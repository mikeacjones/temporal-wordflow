package bootstrap

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/mjones/temporal-word-game/internal/workflows"
)

func TestTemporalFoundationsCampaignIsValid(t *testing.T) {
	configured, err := DefaultWordflowCampaign()
	if err != nil {
		t.Fatal(err)
	}
	if configured.Definition.ID != "temporal-foundations" || len(configured.Levels) != 30 {
		t.Fatalf("unexpected campaign: %q with %d levels", configured.Definition.ID, len(configured.Levels))
	}
	if configured.Definition.EndsAt != nil {
		t.Fatal("the default campaign must not expire")
	}

	validateCampaignLevels(t, configured.Levels, progressiveWordCount)
}

func TestPalmSpringsCampaignIsValid(t *testing.T) {
	contents, err := os.ReadFile("../../deploy/terraform/campaigns/palm-springs-offsite-2026.json")
	if err != nil {
		t.Fatal(err)
	}
	var configured workflows.WordflowCampaignWorkflowInput
	if err := json.Unmarshal(contents, &configured); err != nil {
		t.Fatal(err)
	}
	if configured.Definition.ID != "palm-springs-offsite-2026" || len(configured.Levels) != 12 {
		t.Fatalf("unexpected campaign: %q with %d levels", configured.Definition.ID, len(configured.Levels))
	}

	validateCampaignLevels(t, configured.Levels, progressiveWordCount)
}

func TestDailyChallengeCampaignIsValid(t *testing.T) {
	contents, err := os.ReadFile("../../campaigns/daily-challenge-2026-09-21.json")
	if err != nil {
		t.Fatal(err)
	}
	configured, err := ParseWordflowCampaign(contents)
	if err != nil {
		t.Fatal(err)
	}
	if configured.Definition.ID != "daily-challenge-2026-09-21" || len(configured.Levels) != 3 {
		t.Fatalf("unexpected campaign: %q with %d levels", configured.Definition.ID, len(configured.Levels))
	}
	if configured.Definition.Kind != "daily-challenge" || !configured.Definition.SingleAttempt {
		t.Fatal("daily challenge must be typed and limited to one attempt")
	}
	for _, puzzle := range configured.Levels {
		if len(puzzle.Words) != 10 || len([]rune(puzzle.Letters)) != game.MaxWordflowLetters {
			t.Fatalf("level %d is not an expert puzzle", puzzle.Level)
		}
		if puzzle.TimeLimitSeconds != 180 || puzzle.EffectiveBasePoints() != 20 {
			t.Fatalf("level %d has incorrect daily gameplay settings", puzzle.Level)
		}
		if puzzle.EffectiveHintPrices() != (game.HintPrices{Letter: 15, Brush: 30, Word: 45}) {
			t.Fatalf("level %d has incorrect hint prices", puzzle.Level)
		}
	}
}

func progressiveWordCount(index int) int {
	switch {
	case index < 2:
		return 4
	case index < 5:
		return 6
	case index < 8:
		return 8
	default:
		return 10
	}
}

func progressiveLetterLimit(index int) int {
	switch {
	case index < 2:
		return 4
	case index < 5:
		return 5
	case index < 8:
		return 6
	case index < 11:
		return 7
	default:
		return game.MaxWordflowLetters
	}
}

func validateCampaignLevels(t *testing.T, levels []game.Puzzle, expectedWords func(int) int) {
	t.Helper()
	for index, puzzle := range levels {
		if puzzle.Level != index+1 || len(puzzle.Words) != expectedWords(index) {
			t.Fatalf("level %d has invalid metadata or word count", index+1)
		}
		letterCount := len([]rune(puzzle.Letters))
		if letterCount > progressiveLetterLimit(index) {
			t.Fatalf("level %d has %d letters; this stage allows at most %d", puzzle.Level, letterCount, progressiveLetterLimit(index))
		}
		answers := map[string]bool{}
		cells := map[game.Position]rune{}
		directions := map[game.Position]map[game.Direction]string{}
		usesFullQueue := false
		for _, word := range puzzle.Words {
			if len([]rune(word.Answer)) < 3 || answers[word.Answer] || !spellsFrom(word.Answer, puzzle.Letters) {
				t.Fatalf("level %d has invalid answer %q", puzzle.Level, word.Answer)
			}
			usesFullQueue = usesFullQueue || len([]rune(word.Answer)) == letterCount
			answers[word.Answer] = true
			for character, letter := range word.Answer {
				position := wordPosition(word, character)
				if existing, ok := cells[position]; ok && existing != letter {
					t.Fatalf("level %d has conflicting letters at %+v", puzzle.Level, position)
				}
				if directions[position] == nil {
					directions[position] = map[game.Direction]string{}
				}
				if directions[position][word.Direction] != "" {
					t.Fatalf("level %d has a parallel overlap at %+v", puzzle.Level, position)
				}
				cells[position] = letter
				directions[position][word.Direction] = word.Answer
			}
		}
		if !usesFullQueue {
			t.Fatalf("level %d has no answer using its full letter queue", puzzle.Level)
		}
		for answer := range answers {
			if answers[answer+"S"] || answers[answer+"ES"] {
				t.Fatalf("level %d pads its theme with a plural form of %q", puzzle.Level, answer)
			}
		}
	}
}

func spellsFrom(word, letters string) bool {
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

func wordPosition(word game.PlacedWord, index int) game.Position {
	position := game.Position{Row: word.Row, Col: word.Col}
	if word.Direction == game.Across {
		position.Col += index
	} else {
		position.Row += index
	}
	return position
}
