package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"go.temporal.io/sdk/temporal"
)

const (
	dailyChallengeToolName     = "create_daily_wordflow_challenge"
	defaultDailyChallengeModel = "gpt-5.4-mini"
	dailyChallengeToolAttempts = 5
	openAIModelEnvironment     = "OPENAI_MODEL"
	openAIKeyEnvironment       = "OPENAI_API_KEY"
)

type responseCreator interface {
	New(context.Context, responses.ResponseNewParams, ...option.RequestOption) (*responses.Response, error)
}

type DailyChallenges struct {
	Responses responseCreator
	Model     string
}

type generatedDailyChallenge struct {
	Description string                   `json:"description"`
	Levels      []generatedWordflowLevel `json:"levels"`
}

type generatedWordflowLevel struct {
	Title   string   `json:"title"`
	Letters string   `json:"letters"`
	Words   []string `json:"words"`
}

func (a *DailyChallenges) GenerateDailyWordflowChallenge(ctx context.Context, input workflows.GenerateDailyWordflowChallengeActivityInput) (workflows.WordflowCampaignWorkflowInput, error) {
	creator := a.Responses
	if creator == nil {
		if strings.TrimSpace(os.Getenv(openAIKeyEnvironment)) == "" {
			return workflows.WordflowCampaignWorkflowInput{}, temporal.NewNonRetryableApplicationError(
				openAIKeyEnvironment+" is required to generate a daily challenge",
				"openai_not_configured",
				nil,
			)
		}
		client := openai.NewClient()
		creator = &client.Responses
	}

	model := strings.TrimSpace(a.Model)
	if model == "" {
		model = strings.TrimSpace(os.Getenv(openAIModelEnvironment))
	}
	if model == "" {
		model = defaultDailyChallengeModel
	}

	prompt := dailyChallengePrompt(input)
	params := responses.ResponseNewParams{
		Model:        model,
		Instructions: openai.String(dailyChallengeAgentInstructions),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: openai.String(prompt),
		},
		MaxOutputTokens:   openai.Int(5000),
		ParallelToolCalls: openai.Bool(false),
		Store:             openai.Bool(false),
		Tools: []responses.ToolUnionParam{{
			OfFunction: &responses.FunctionToolParam{
				Name:        dailyChallengeToolName,
				Description: openai.String("Submit the complete daily Wordflow challenge and end the generation turn."),
				Parameters:  dailyChallengeToolSchema(),
				Strict:      openai.Bool(true),
			},
		}},
		ToolChoice: responses.ResponseNewParamsToolChoiceUnion{
			OfFunctionTool: &responses.ToolChoiceFunctionParam{Name: dailyChallengeToolName},
		},
	}

	var history responses.ResponseInputParam
	var lastErr error
	for attempt := 1; attempt <= dailyChallengeToolAttempts; attempt++ {
		response, err := creator.New(ctx, params)
		if err != nil {
			return workflows.WordflowCampaignWorkflowInput{}, fmt.Errorf("generate daily challenge with OpenAI: %w", err)
		}

		generated, call, err := extractDailyChallengeToolCall(response)
		if err == nil {
			if configured, buildErr := buildDailyChallengeCampaign(input, generated); buildErr == nil {
				return configured, nil
			} else {
				err = buildErr
			}
		}
		lastErr = err
		if call.CallID == "" || attempt == dailyChallengeToolAttempts {
			break
		}

		if len(history) == 0 {
			history = append(history, responses.ResponseInputItemParamOfMessage(prompt, responses.EasyInputMessageRoleUser))
		}
		history = append(history, responses.ResponseInputItemParamOfFunctionCall(call.Arguments, call.CallID, dailyChallengeToolName))
		toolOutput := responses.ResponseInputItemParamOfFunctionCallOutput(fmt.Sprintf(
			`{"accepted":false,"error":%q,"instruction":"Correct the rejected challenge and call create_daily_wordflow_challenge again."}`,
			err.Error(),
		))
		toolOutput.OfFunctionCallOutput.CallID = openai.String(call.CallID)
		history = append(history, toolOutput)
		params.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: history}
	}
	return workflows.WordflowCampaignWorkflowInput{}, fmt.Errorf("OpenAI did not produce a valid daily challenge after %d tool calls: %w", dailyChallengeToolAttempts, lastErr)
}

const dailyChallengeAgentInstructions = `You design expert Wordflow puzzles. The only way to finish your turn is to call the create_daily_wordflow_challenge tool exactly once. Do not return prose or any other final response.`

func dailyChallengePrompt(input workflows.GenerateDailyWordflowChallengeActivityInput) string {
	exclusions := "There are no recent answers to exclude."
	if len(input.ExcludedWords) > 0 {
		exclusions = "Do not use any of these answers from the previous ten daily challenges: " + strings.Join(input.ExcludedWords, ", ") + "."
	}
	return fmt.Sprintf(`Create the Daily Wordflow Challenge for %s.

The tool call must define exactly three especially difficult but fair levels. For every level:
- provide exactly eight uppercase letters and exactly ten unique English answers
- every answer must be at least three letters and spellable from the letter multiset without using a letter too many times
- choose a familiar eight-letter anchor word first, use exactly its letters for the letters field in any order, and include that anchor in the answers; the anchor must be a common standalone word, not a plural, conjugation, obscure derivative, or invented compound
- the letters field is only a letter bank and need not itself spell a word; every entry in words, including the eight-letter anchor, must be a real familiar word
- make the words interconnected enough to form one connected crossword
- use familiar modern dictionary words; never use proper nouns, abbreviations, offensive terms, archaic words, or dubious comparative forms
- do not pad the answer list with both a base word and its simple plural or past tense, and do not reuse an answer in another level
- give the level a short, energetic title

Before calling the tool, silently audit every answer character by character against its eight-letter multiset and replace anything questionable. The tool arguments are rejected automatically if even one answer overuses a letter or the crossword cannot be connected.

%s

The campaign is one attempt only. All three levels unlock immediately but must be played sequentially. Each level has a hard 180-second limit, awards 20 base points, and prices letter, brush, and word hints at 15, 30, and 45 points. Its active window is %s through %s. Mention the high difficulty, time pressure, richer rewards, premium hints, and single attempt in one concise campaign description.

End your turn by calling create_daily_wordflow_challenge exactly once.`,
		input.Date,
		exclusions,
		input.StartsAt.Format("2006-01-02 15:04 MST"),
		input.EndsAt.Format("2006-01-02 15:04 MST"),
	)
}

func dailyChallengeToolSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"description": map[string]any{
				"type":      "string",
				"minLength": 40,
				"maxLength": 220,
			},
			"levels": map[string]any{
				"type":     "array",
				"minItems": 3,
				"maxItems": 3,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"title": map[string]any{
							"type":      "string",
							"minLength": 3,
							"maxLength": 40,
						},
						"letters": map[string]any{
							"type":      "string",
							"pattern":   "^[A-Z]{8}$",
							"minLength": 8,
							"maxLength": 8,
						},
						"words": map[string]any{
							"type":     "array",
							"minItems": 10,
							"maxItems": 10,
							"items": map[string]any{
								"type":      "string",
								"pattern":   "^[A-Z]{3,8}$",
								"minLength": 3,
								"maxLength": 8,
							},
						},
					},
					"required": []string{"title", "letters", "words"},
				},
			},
		},
		"required": []string{"description", "levels"},
	}
}

type dailyChallengeToolCall struct {
	CallID    string
	Arguments string
}

func extractDailyChallengeToolCall(response *responses.Response) (generatedDailyChallenge, dailyChallengeToolCall, error) {
	if response == nil {
		return generatedDailyChallenge{}, dailyChallengeToolCall{}, fmt.Errorf("OpenAI returned no response")
	}

	var call dailyChallengeToolCall
	for _, item := range response.Output {
		switch item.Type {
		case "function_call":
			functionCall := item.AsFunctionCall()
			if functionCall.Name != dailyChallengeToolName {
				return generatedDailyChallenge{}, call, fmt.Errorf("OpenAI called unexpected tool %q", functionCall.Name)
			}
			if call.Arguments != "" {
				return generatedDailyChallenge{}, call, fmt.Errorf("OpenAI called %s more than once", dailyChallengeToolName)
			}
			call = dailyChallengeToolCall{CallID: functionCall.CallID, Arguments: functionCall.Arguments}
		case "message":
			return generatedDailyChallenge{}, call, fmt.Errorf("OpenAI returned a message instead of ending with %s", dailyChallengeToolName)
		}
	}
	if call.Arguments == "" {
		return generatedDailyChallenge{}, call, fmt.Errorf("OpenAI did not call %s", dailyChallengeToolName)
	}

	var generated generatedDailyChallenge
	if err := json.Unmarshal([]byte(call.Arguments), &generated); err != nil {
		return generatedDailyChallenge{}, call, fmt.Errorf("decode %s arguments: %w", dailyChallengeToolName, err)
	}
	return generated, call, nil
}

func buildDailyChallengeCampaign(input workflows.GenerateDailyWordflowChallengeActivityInput, generated generatedDailyChallenge) (workflows.WordflowCampaignWorkflowInput, error) {
	if input.Date == "" || input.StartsAt.IsZero() || input.EndsAt.IsZero() || !input.EndsAt.After(input.StartsAt) {
		return workflows.WordflowCampaignWorkflowInput{}, temporal.NewNonRetryableApplicationError(
			"daily challenge date and active window are required", "invalid_daily_challenge_input", nil,
		)
	}
	generated.Description = strings.TrimSpace(generated.Description)
	if generated.Description == "" {
		return workflows.WordflowCampaignWorkflowInput{}, fmt.Errorf("daily challenge description is required")
	}
	if len(generated.Levels) != 3 {
		return workflows.WordflowCampaignWorkflowInput{}, fmt.Errorf("daily challenge must contain exactly 3 levels")
	}

	levels := make([]game.Puzzle, 0, len(generated.Levels))
	allAnswers := make(map[string]bool, len(input.ExcludedWords)+30)
	for _, answer := range input.ExcludedWords {
		allAnswers[strings.ToUpper(strings.TrimSpace(answer))] = true
	}
	for index, level := range generated.Levels {
		definition, err := validateGeneratedWordflowLevel(index+1, level, allAnswers)
		if err != nil {
			return workflows.WordflowCampaignWorkflowInput{}, err
		}
		puzzle, err := game.BuildPuzzle(index+1, definition)
		if err != nil {
			return workflows.WordflowCampaignWorkflowInput{}, fmt.Errorf("build generated daily challenge: %w", err)
		}
		levels = append(levels, puzzle)
	}

	startsAt := input.StartsAt
	endsAt := input.EndsAt
	return workflows.WordflowCampaignWorkflowInput{
		Definition: campaign.Definition{
			ID:            "daily-challenge-" + input.Date,
			Kind:          campaign.KindDailyChallenge,
			SingleAttempt: true,
			Game: campaign.GameSummary{
				ID: campaign.GameWordflow, Title: "Wordflow",
				Description: "Resolve connected words from a shared letter queue.",
			},
			Title:       "Daily Challenge · " + startsAt.Format("January 2"),
			Description: generated.Description,
			StartsAt:    &startsAt,
			EndsAt:      &endsAt,
			Unlock:      campaign.UnlockPolicy{InitialLevels: 3},
		},
		Levels: levels,
	}, nil
}

func validateGeneratedWordflowLevel(levelNumber int, generated generatedWordflowLevel, allAnswers map[string]bool) (game.LevelDefinition, error) {
	title := strings.TrimSpace(generated.Title)
	letters := strings.ToUpper(strings.TrimSpace(generated.Letters))
	if title == "" {
		return game.LevelDefinition{}, fmt.Errorf("level %d title is required", levelNumber)
	}
	if len([]rune(letters)) != game.MaxWordflowLetters || !asciiLettersOnly(letters) {
		return game.LevelDefinition{}, fmt.Errorf("level %d must have exactly %d uppercase letters", levelNumber, game.MaxWordflowLetters)
	}
	if len(generated.Words) != 10 {
		return game.LevelDefinition{}, fmt.Errorf("level %d must have exactly 10 words", levelNumber)
	}

	words := make([]string, 0, len(generated.Words))
	levelAnswers := map[string]bool{}
	usesFullQueue := false
	for _, value := range generated.Words {
		answer := strings.ToUpper(strings.TrimSpace(value))
		length := len([]rune(answer))
		switch {
		case length < 3 || length > game.MaxWordflowLetters || !asciiLettersOnly(answer):
			return game.LevelDefinition{}, fmt.Errorf("level %d has invalid answer %q", levelNumber, value)
		case levelAnswers[answer]:
			return game.LevelDefinition{}, fmt.Errorf("level %d repeats answer %q", levelNumber, answer)
		case allAnswers[answer]:
			return game.LevelDefinition{}, fmt.Errorf("daily challenge reuses answer %q", answer)
		case !spellsFromLetters(answer, letters):
			return game.LevelDefinition{}, fmt.Errorf("level %d answer %q cannot be spelled from %q", levelNumber, answer, letters)
		}
		levelAnswers[answer] = true
		allAnswers[answer] = true
		usesFullQueue = usesFullQueue || length == game.MaxWordflowLetters
		words = append(words, answer)
	}
	if !usesFullQueue {
		return game.LevelDefinition{}, fmt.Errorf("level %d has no answer using all %d letters", levelNumber, game.MaxWordflowLetters)
	}
	for answer := range levelAnswers {
		if levelAnswers[answer+"S"] || levelAnswers[answer+"ES"] {
			return game.LevelDefinition{}, fmt.Errorf("level %d pads its answers with a plural form of %q", levelNumber, answer)
		}
		pastTense := answer + "ED"
		if strings.HasSuffix(answer, "E") {
			pastTense = answer + "D"
		}
		if levelAnswers[pastTense] {
			return game.LevelDefinition{}, fmt.Errorf("level %d pads its answers with a past-tense form of %q", levelNumber, answer)
		}
	}

	return game.LevelDefinition{
		Title:            title,
		Letters:          letters,
		Words:            words,
		TimeLimitSeconds: 180,
		BasePoints:       20,
		HintPrices:       game.HintPrices{Letter: 15, Brush: 30, Word: 45},
	}, nil
}

func asciiLettersOnly(value string) bool {
	for _, letter := range value {
		if letter > unicode.MaxASCII || letter < 'A' || letter > 'Z' {
			return false
		}
	}
	return value != ""
}

func spellsFromLetters(word, letters string) bool {
	available := map[rune]int{}
	for _, letter := range letters {
		available[letter]++
	}
	for _, letter := range word {
		available[letter]--
		if available[letter] < 0 {
			return false
		}
	}
	return true
}
