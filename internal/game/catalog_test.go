package game

import (
	"strings"
	"testing"
)

func TestBuildPuzzleRejectsMoreThanEightLetters(t *testing.T) {
	_, err := BuildPuzzle(1, LevelDefinition{Letters: "ABCDEFGHI", Words: []string{"BAD"}})
	if err == nil || !strings.Contains(err.Error(), "more than 8 letters") {
		t.Fatalf("expected the letter limit error, got %v", err)
	}
}

func TestPlacementRejectsParallelOverlap(t *testing.T) {
	grid := map[gridPoint]gridCell{}
	writeWord(grid, PlacedWord{Answer: "SESAME", Direction: Down})

	if placementFits(PlacedWord{Answer: "SAME", Row: 2, Direction: Down}, grid) {
		t.Fatal("parallel words must not share cells")
	}
	if !placementFits(PlacedWord{Answer: "SEA", Direction: Across}, grid) {
		t.Fatal("perpendicular words may cross at a matching letter")
	}
}

func TestPlacementRejectsTouchingWords(t *testing.T) {
	grid := map[gridPoint]gridCell{}
	writeWord(grid, PlacedWord{Answer: "GAME", Col: 2, Direction: Down})
	writeWord(grid, PlacedWord{Answer: "MOM", Row: 1, Col: 3, Direction: Down})

	if placementFits(PlacedWord{Answer: "SEA", Row: 1, Direction: Across}, grid) {
		t.Fatal("a word must not touch another letter immediately after its end")
	}

	grid = map[gridPoint]gridCell{}
	writeWord(grid, PlacedWord{Answer: "GAME", Col: 2, Direction: Down})
	writeWord(grid, PlacedWord{Answer: "DOG", Row: -2, Col: 1, Direction: Down})
	if placementFits(PlacedWord{Answer: "SEA", Row: 1, Direction: Across}, grid) {
		t.Fatal("a word must not touch other letters along its side")
	}
}

func catalogTestPosition(word PlacedWord, index int) Position {
	position := Position{Row: word.Row, Col: word.Col}
	if word.Direction == Across {
		position.Col += index
	} else {
		position.Row += index
	}
	return position
}

func sideNeighbors(position Position, direction Direction) []Position {
	if direction == Across {
		return []Position{{Row: position.Row - 1, Col: position.Col}, {Row: position.Row + 1, Col: position.Col}}
	}
	return []Position{{Row: position.Row, Col: position.Col - 1}, {Row: position.Row, Col: position.Col + 1}}
}

func spellsFrom(word, letters string) bool {
	counts := map[rune]int{}
	for _, letter := range strings.ToUpper(letters) {
		counts[letter]++
	}
	for _, letter := range strings.ToUpper(word) {
		counts[letter]--
		if counts[letter] < 0 {
			return false
		}
	}
	return true
}
