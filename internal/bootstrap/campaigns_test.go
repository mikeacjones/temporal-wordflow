package bootstrap

import (
	"strings"
	"testing"

	"github.com/mjones/temporal-word-game/internal/game"
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

	for index, puzzle := range configured.Levels {
		if puzzle.Level != index+1 || len(puzzle.Words) < 3 {
			t.Fatalf("level %d has invalid metadata", index+1)
		}
		answers := map[string]bool{}
		cells := map[game.Position]rune{}
		directions := map[game.Position]map[game.Direction]string{}
		for _, word := range puzzle.Words {
			if answers[word.Answer] || !spellsFrom(word.Answer, puzzle.Letters) {
				t.Fatalf("level %d has invalid answer %q", puzzle.Level, word.Answer)
			}
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
