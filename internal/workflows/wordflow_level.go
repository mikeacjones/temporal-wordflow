package workflows

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

type WordflowLevelWorkflowInput struct {
	PlayerID   string              `json:"playerId,omitempty"`
	CampaignID string              `json:"campaignId,omitempty"`
	Puzzle     game.Puzzle         `json:"puzzle,omitempty"`
	State      *WordflowLevelState `json:"state,omitempty"`
}

// WordflowLevelState is the complete Continue-As-New checkpoint for one level.
type WordflowLevelState struct {
	WorkflowID          string             `json:"workflowId"`
	PlayerID            string             `json:"playerId"`
	CampaignID          string             `json:"campaignId"`
	Puzzle              game.Puzzle        `json:"puzzle"`
	StartedAt           time.Time          `json:"startedAt"`
	Letters             string             `json:"letters"`
	FoundAnswers        []string           `json:"foundAnswers"`
	RejectedWords       []string           `json:"rejectedWords"`
	RevealedCells       []game.Position    `json:"revealedCells"`
	Attempts            int                `json:"attempts"`
	IncorrectGuesses    int                `json:"incorrectGuesses"`
	LetterHintsUsed     int                `json:"letterHintsUsed"`
	BrushHintsUsed      int                `json:"brushHintsUsed"`
	WordHintsUsed       int                `json:"wordHintsUsed"`
	Hints               game.HintInventory `json:"hints"`
	Complete            bool               `json:"complete"`
	CompletedAt         time.Time          `json:"completedAt"`
	Score               game.GameScore     `json:"score"`
	ShuffleCount        int                `json:"shuffleCount"`
	EventsSinceContinue int                `json:"eventsSinceContinue"`
	ContinueAfterEvents int                `json:"continueAfterEvents"`
}

type SubmitGuessInput struct {
	Word string `json:"word"`
}

type UseHintInput struct {
	Hint game.HintType `json:"hint"`
}

type ShuffleInput struct{}

const (
	letterHintPointCost = 10
	brushHintPointCost  = 20
	wordHintPointCost   = 30
)

type speedBonusRule struct {
	maximumDuration time.Duration
	points          int
}

var speedBonusRules = []speedBonusRule{
	{maximumDuration: time.Minute, points: 10},
	{maximumDuration: 2 * time.Minute, points: 7},
	{maximumDuration: 3 * time.Minute, points: 4},
}

func WordflowLevelWorkflow(ctx workflow.Context, input WordflowLevelWorkflowInput) (campaign.LevelResult, error) {
	state := initialWordflowLevelState(ctx, input)
	// Paid hints await an Activity, so every level mutation shares this lock.
	lock := workflow.NewMutex(ctx)
	changed := workflow.NewBufferedChannel(ctx, 1)

	if err := workflow.SetQueryHandler(ctx, QueryWordflowLevelState, func() (game.GameView, error) {
		return wordflowLevelView(state), nil
	}); err != nil {
		return campaign.LevelResult{}, err
	}

	if err := workflow.SetUpdateHandler(ctx, UpdateSubmitGuess, func(updateCtx workflow.Context, update SubmitGuessInput) (game.GuessResult, error) {
		if err := lock.Lock(updateCtx); err != nil {
			return game.GuessResult{}, err
		}
		defer lock.Unlock()

		if state.Complete {
			return game.GuessResult{Outcome: "complete", Game: wordflowLevelView(state)}, nil
		}

		guess := strings.ToUpper(strings.TrimSpace(update.Word))
		if guess == "" {
			return game.GuessResult{}, temporal.NewApplicationError("guess cannot be empty", "invalid_guess")
		}

		state.Attempts++
		outcome := "not_in_puzzle"
		if !canSpell(guess, state.Puzzle.Letters) {
			outcome = "invalid_letters"
		} else if hasAnswer(state.FoundAnswers, guess) {
			outcome = "already_found"
		} else if puzzleHasAnswer(state.Puzzle, guess) {
			state.FoundAnswers = append(state.FoundAnswers, guess)
			outcome = "found"
		}
		if outcome == "not_in_puzzle" {
			state.RejectedWords = append([]string{guess}, state.RejectedWords...)
		}
		if outcome != "found" {
			state.IncorrectGuesses++
		}

		finishLevelIfSolved(state, workflow.Now(updateCtx))
		recordWordflowLevelMutation(state, changed)
		return game.GuessResult{Outcome: outcome, Game: wordflowLevelView(state)}, nil
	}); err != nil {
		return campaign.LevelResult{}, err
	}

	if err := workflow.SetUpdateHandler(ctx, UpdateUseHint, func(updateCtx workflow.Context, update UseHintInput) (game.HintResult, error) {
		if err := lock.Lock(updateCtx); err != nil {
			return game.HintResult{}, err
		}
		defer lock.Unlock()

		if state.Complete {
			return game.HintResult{Outcome: "complete", Game: wordflowLevelView(state)}, nil
		}

		if err := validateHintTarget(state, update.Hint); err != nil {
			return game.HintResult{}, err
		}

		pointsSpent := 0
		var pointsRemaining *int
		if freeHintCount(state, update.Hint) == 0 {
			price, err := hintPointCost(update.Hint)
			if err != nil {
				return game.HintResult{}, err
			}
			updateID := fmt.Sprintf("spend/%s/%s", state.WorkflowID, workflow.GetCurrentUpdateInfo(updateCtx).ID)
			activityCtx := workflow.WithActivityOptions(updateCtx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second})
			var spend SpendPointsResult
			err = workflow.ExecuteActivity(activityCtx, ActivitySpendPoints, SpendPointsActivityInput{
				PlayerWorkflowID: PlayerWorkflowID(state.PlayerID),
				UpdateID:         updateID,
				Spend: SpendPointsInput{
					LevelWorkflowID: state.WorkflowID,
					Amount:          price,
					Reason:          "wordflow/" + string(update.Hint) + "-hint",
				},
			}).Get(updateCtx, &spend)
			if err != nil {
				return game.HintResult{}, err
			}
			pointsSpent = spend.Spent
			pointsRemaining = &spend.Remaining
		}

		outcome, err := applyHint(state, update.Hint, pointsSpent > 0)
		if err != nil {
			return game.HintResult{}, err
		}
		recordHintUse(state, update.Hint)
		finishLevelIfSolved(state, workflow.Now(updateCtx))
		recordWordflowLevelMutation(state, changed)
		return game.HintResult{
			Outcome: outcome, Game: wordflowLevelView(state),
			PointsSpent: pointsSpent, PointsRemaining: pointsRemaining,
		}, nil
	}); err != nil {
		return campaign.LevelResult{}, err
	}

	if err := workflow.SetUpdateHandler(ctx, UpdateShuffle, func(updateCtx workflow.Context, _ ShuffleInput) (game.GameView, error) {
		if err := lock.Lock(updateCtx); err != nil {
			return game.GameView{}, err
		}
		defer lock.Unlock()
		if state.Complete {
			return wordflowLevelView(state), nil
		}

		state.ShuffleCount++
		state.Letters = shuffleLetters(state.Puzzle.Letters, state.ShuffleCount)
		recordWordflowLevelMutation(state, changed)
		return wordflowLevelView(state), nil
	}); err != nil {
		return campaign.LevelResult{}, err
	}

	for !state.Complete {
		var ignored bool
		changed.Receive(ctx, &ignored)
		if shouldContinueWordflowLevel(ctx, state) {
			if err := workflow.Await(ctx, func() bool { return workflow.AllHandlersFinished(ctx) }); err != nil {
				return campaign.LevelResult{}, err
			}
			state.EventsSinceContinue = 0
			return campaign.LevelResult{}, continueWordflowLevelAsNew(ctx, state)
		}
	}

	if err := workflow.Await(ctx, func() bool { return workflow.AllHandlersFinished(ctx) }); err != nil {
		return campaign.LevelResult{}, err
	}

	result := campaign.LevelResult{
		GameID: campaign.GameWordflow, CampaignID: state.CampaignID,
		Level: state.Puzzle.Level, Attempts: state.Attempts, CompletedAt: state.CompletedAt,
		Awards: []campaign.PointAward{{Description: "Wordflow score", Points: state.Score.Points}},
	}
	if state.Puzzle.CompletionBonus > 0 {
		result.Awards = append(result.Awards, campaign.PointAward{
			Description: state.Puzzle.CompletionBonusName,
			Points:      state.Puzzle.CompletionBonus,
		})
	}
	if state.Puzzle.SpecialEvent != nil {
		result.Awards = append(result.Awards, campaign.PointAward{
			Description: state.Puzzle.SpecialEvent.Name,
			Points:      state.Puzzle.SpecialEvent.BonusPoints,
		})
	}
	return result, nil
}

func initialWordflowLevelState(ctx workflow.Context, input WordflowLevelWorkflowInput) *WordflowLevelState {
	if input.State != nil {
		state := input.State
		if state.StartedAt.IsZero() {
			state.StartedAt = workflow.Now(ctx)
		}
		if state.ContinueAfterEvents == 0 {
			state.ContinueAfterEvents = defaultContinueAfterEvents
		}
		return state
	}

	now := workflow.Now(ctx)
	return &WordflowLevelState{
		WorkflowID:          workflow.GetInfo(ctx).WorkflowExecution.ID,
		PlayerID:            input.PlayerID,
		CampaignID:          input.CampaignID,
		Puzzle:              input.Puzzle,
		StartedAt:           now,
		Letters:             input.Puzzle.Letters,
		Hints:               game.HintInventory{Letters: 2, Brushes: 1, Words: 1},
		ContinueAfterEvents: defaultContinueAfterEvents,
	}
}

func wordflowLevelView(state *WordflowLevelState) game.GameView {
	hinted := map[game.Position]bool{}
	for _, position := range state.RevealedCells {
		hinted[position] = true
	}

	foundCells := map[game.Position]bool{}
	cells := map[game.Position]string{}
	positions := make([]game.Position, 0)
	words := make([]game.WordView, 0, len(state.Puzzle.Words))
	for _, placed := range state.Puzzle.Words {
		found := hasAnswer(state.FoundAnswers, placed.Answer)
		wordView := game.WordView{
			Row: placed.Row, Col: placed.Col, Direction: placed.Direction,
			Length: len(placed.Answer), Found: found,
		}
		for index, letter := range placed.Answer {
			position := positionFor(placed, index)
			if _, present := cells[position]; !present {
				positions = append(positions, position)
			}
			cells[position] = string(letter)
			wordView.Cells = append(wordView.Cells, position)
			if found {
				foundCells[position] = true
			}
		}
		words = append(words, wordView)
	}

	sort.Slice(positions, func(i, j int) bool {
		if positions[i].Row == positions[j].Row {
			return positions[i].Col < positions[j].Col
		}
		return positions[i].Row < positions[j].Row
	})

	cellViews := make([]game.CellView, 0, len(positions))
	for _, position := range positions {
		cell := game.CellView{
			Row: position.Row, Col: position.Col,
			Revealed: foundCells[position] || hinted[position],
			Hinted:   hinted[position] && !foundCells[position],
		}
		if cell.Revealed {
			cell.Letter = cells[position]
		}
		cellViews = append(cellViews, cell)
	}

	view := game.GameView{
		WorkflowID: state.WorkflowID, PlayerID: state.PlayerID, CampaignID: state.CampaignID,
		Level: state.Puzzle.Level, Title: state.Puzzle.Title, Letters: state.Letters,
		Cells: cellViews, Words: words, FoundWords: len(state.FoundAnswers), TotalWords: len(state.Puzzle.Words),
		Attempts: state.Attempts, RejectedWords: append([]string(nil), state.RejectedWords...),
		SpeedBonuses: speedBonusTiers(state.StartedAt),
		HintBonus:    hintBonusFor(state), AccuracyBonus: accuracyBonusFor(state.IncorrectGuesses), Hints: state.Hints,
		HintPrices: game.HintPrices{Letter: letterHintPointCost, Brush: brushHintPointCost, Word: wordHintPointCost},
		Complete:   state.Complete, SpecialEvent: state.Puzzle.SpecialEvent,
	}
	if state.Complete {
		completedAt := state.CompletedAt
		view.CompletedAt = &completedAt
		score := state.Score
		view.Score = &score
	}
	return view
}

func recordHintUse(state *WordflowLevelState, hint game.HintType) {
	switch hint {
	case game.HintLetter:
		state.LetterHintsUsed++
	case game.HintBrush:
		state.BrushHintsUsed++
	case game.HintWord:
		state.WordHintsUsed++
	}
}

func applyHint(state *WordflowLevelState, hint game.HintType, paid bool) (string, error) {
	if err := validateHintTarget(state, hint); err != nil {
		return "", err
	}

	switch hint {
	case game.HintLetter:
		if state.Hints.Letters == 0 && !paid {
			return "", temporal.NewApplicationError("no single-letter hints remain", "no_hint")
		}
		if state.Hints.Letters > 0 {
			state.Hints.Letters--
		}
		word, _ := firstUnsolvedWordWithHiddenCell(state)
		revealLetters(state, word, 1)
		return "letter_revealed", nil
	case game.HintBrush:
		if state.Hints.Brushes == 0 && !paid {
			return "", temporal.NewApplicationError("no paintbrush hints remain", "no_hint")
		}
		if state.Hints.Brushes > 0 {
			state.Hints.Brushes--
		}
		word, _ := firstUnsolvedWordWithHiddenCell(state)
		revealLetters(state, word, 2)
		return "letters_revealed", nil
	case game.HintWord:
		if state.Hints.Words == 0 && !paid {
			return "", temporal.NewApplicationError("no whole-word hints remain", "no_hint")
		}
		if state.Hints.Words > 0 {
			state.Hints.Words--
		}
		word, _ := firstUnsolvedWord(state)
		state.FoundAnswers = append(state.FoundAnswers, word.Answer)
		return "word_revealed", nil
	default:
		return "", temporal.NewApplicationError("unknown hint type", "invalid_hint")
	}
}

func validateHintTarget(state *WordflowLevelState, hint game.HintType) error {
	switch hint {
	case game.HintLetter, game.HintBrush:
		if _, ok := firstUnsolvedWordWithHiddenCell(state); !ok {
			return temporal.NewApplicationError("all remaining letters are already visible", "no_hidden_letters")
		}
	case game.HintWord:
		if _, ok := firstUnsolvedWord(state); !ok {
			return temporal.NewApplicationError("all words are already resolved", "no_hidden_words")
		}
	default:
		return temporal.NewApplicationError("unknown hint type", "invalid_hint")
	}
	return nil
}

func freeHintCount(state *WordflowLevelState, hint game.HintType) int {
	switch hint {
	case game.HintLetter:
		return state.Hints.Letters
	case game.HintBrush:
		return state.Hints.Brushes
	case game.HintWord:
		return state.Hints.Words
	default:
		return 0
	}
}

func hintPointCost(hint game.HintType) (int, error) {
	switch hint {
	case game.HintLetter:
		return letterHintPointCost, nil
	case game.HintBrush:
		return brushHintPointCost, nil
	case game.HintWord:
		return wordHintPointCost, nil
	default:
		return 0, temporal.NewApplicationError("unknown hint type", "invalid_hint")
	}
}

func firstUnsolvedWordWithHiddenCell(state *WordflowLevelState) (game.PlacedWord, bool) {
	for _, word := range state.Puzzle.Words {
		if hasAnswer(state.FoundAnswers, word.Answer) {
			continue
		}
		for index := range word.Answer {
			if !positionVisible(state, positionFor(word, index)) {
				return word, true
			}
		}
	}
	return game.PlacedWord{}, false
}

func firstUnsolvedWord(state *WordflowLevelState) (game.PlacedWord, bool) {
	for _, word := range state.Puzzle.Words {
		if !hasAnswer(state.FoundAnswers, word.Answer) {
			return word, true
		}
	}
	return game.PlacedWord{}, false
}

func revealLetters(state *WordflowLevelState, word game.PlacedWord, count int) {
	for index := range word.Answer {
		position := positionFor(word, index)
		if positionVisible(state, position) {
			continue
		}
		state.RevealedCells = append(state.RevealedCells, position)
		count--
		if count == 0 {
			return
		}
	}
}

func positionVisible(state *WordflowLevelState, position game.Position) bool {
	if hasPosition(state.RevealedCells, position) {
		return true
	}
	for _, word := range state.Puzzle.Words {
		if !hasAnswer(state.FoundAnswers, word.Answer) {
			continue
		}
		for index := range word.Answer {
			if positionFor(word, index) == position {
				return true
			}
		}
	}
	return false
}

func positionFor(word game.PlacedWord, index int) game.Position {
	position := game.Position{Row: word.Row, Col: word.Col}
	if word.Direction == game.Across {
		position.Col += index
	} else {
		position.Row += index
	}
	return position
}

func finishLevelIfSolved(state *WordflowLevelState, now time.Time) {
	if len(state.FoundAnswers) == len(state.Puzzle.Words) {
		state.Complete = true
		state.CompletedAt = now
		state.Score = calculateGameScore(state)
	}
}

func calculateGameScore(state *WordflowLevelState) game.GameScore {
	duration := state.CompletedAt.Sub(state.StartedAt)
	if duration < 0 {
		duration = 0
	}

	speedBonus := speedBonusFor(duration)
	accuracyBonus := accuracyBonusFor(state.IncorrectGuesses)
	hintBonus := hintBonusFor(state)
	basePoints := 10

	return game.GameScore{
		Points:           basePoints + speedBonus + accuracyBonus + hintBonus,
		BasePoints:       basePoints,
		SpeedBonus:       speedBonus,
		AccuracyBonus:    accuracyBonus,
		HintBonus:        hintBonus,
		DurationSeconds:  int64(duration / time.Second),
		IncorrectGuesses: state.IncorrectGuesses,
		HintsUsed:        state.LetterHintsUsed + state.BrushHintsUsed + state.WordHintsUsed,
	}
}

func accuracyBonusFor(incorrectGuesses int) int {
	return max(0, 10-(2*incorrectGuesses))
}

func hintBonusFor(state *WordflowLevelState) int {
	penalty := (2 * state.LetterHintsUsed) + (4 * state.BrushHintsUsed) + (8 * state.WordHintsUsed)
	return max(0, 10-penalty)
}

func speedBonusFor(duration time.Duration) int {
	for _, rule := range speedBonusRules {
		if duration <= rule.maximumDuration {
			return rule.points
		}
	}
	return 0
}

func speedBonusTiers(startedAt time.Time) []game.SpeedBonusTier {
	tiers := make([]game.SpeedBonusTier, 0, len(speedBonusRules))
	for _, rule := range speedBonusRules {
		tiers = append(tiers, game.SpeedBonusTier{
			Points: rule.points,
			EndsAt: startedAt.Add(rule.maximumDuration),
		})
	}
	return tiers
}

func puzzleHasAnswer(puzzle game.Puzzle, answer string) bool {
	for _, word := range puzzle.Words {
		if word.Answer == answer {
			return true
		}
	}
	return false
}

func hasAnswer(answers []string, answer string) bool {
	for _, existing := range answers {
		if existing == answer {
			return true
		}
	}
	return false
}

func hasPosition(positions []game.Position, position game.Position) bool {
	for _, existing := range positions {
		if existing == position {
			return true
		}
	}
	return false
}

func canSpell(word, letters string) bool {
	available := map[rune]int{}
	for _, letter := range strings.ToUpper(letters) {
		available[letter]++
	}
	for _, letter := range strings.ToUpper(word) {
		available[letter]--
		if available[letter] < 0 {
			return false
		}
	}
	return true
}

func shuffleLetters(letters string, count int) string {
	runes := []rune(strings.ToUpper(letters))
	if len(runes) < 2 {
		return strings.ToUpper(letters)
	}
	shift := count % len(runes)
	runes = append(append([]rune(nil), runes[shift:]...), runes[:shift]...)
	if count%2 == 1 {
		for left, right := 0, len(runes)-1; left < right; left, right = left+1, right-1 {
			runes[left], runes[right] = runes[right], runes[left]
		}
	}
	return string(runes)
}

func recordWordflowLevelMutation(state *WordflowLevelState, changed workflow.SendChannel) {
	state.EventsSinceContinue++
	changed.SendAsync(true)
}

func shouldContinueWordflowLevel(ctx workflow.Context, state *WordflowLevelState) bool {
	return state.EventsSinceContinue >= state.ContinueAfterEvents ||
		workflow.GetInfo(ctx).GetContinueAsNewSuggested() ||
		workflow.GetInfo(ctx).GetTargetWorkerDeploymentVersionChanged()
}

func continueWordflowLevelAsNew(ctx workflow.Context, state *WordflowLevelState) error {
	options := workflow.ContinueAsNewErrorOptions{}
	if workflow.GetInfo(ctx).GetTargetWorkerDeploymentVersionChanged() {
		options.InitialVersioningBehavior = workflow.ContinueAsNewVersioningBehaviorAutoUpgrade
	}
	return workflow.NewContinueAsNewErrorWithOptions(ctx, options, WordflowLevelWorkflowName, WordflowLevelWorkflowInput{State: state})
}
