package game

import (
	"fmt"
	"sort"
	"strings"
)

type LevelDefinition struct {
	Title               string        `json:"title"`
	Letters             string        `json:"letters"`
	Words               []string      `json:"words"`
	SpecialEvent        *SpecialEvent `json:"specialEvent,omitempty"`
	CompletionBonus     int           `json:"completionBonus,omitempty"`
	CompletionBonusName string        `json:"completionBonusName,omitempty"`
}

func BuildPuzzle(level int, definition LevelDefinition) (Puzzle, error) {
	if len([]rune(definition.Letters)) > MaxWordflowLetters {
		return Puzzle{}, fmt.Errorf("level %d has more than %d letters", level, MaxWordflowLetters)
	}
	words := append([]string(nil), definition.Words...)
	sort.SliceStable(words, func(i, j int) bool {
		return len(words[i]) > len(words[j])
	})

	placed, err := placeWords(words)
	if err != nil {
		return Puzzle{}, fmt.Errorf("level %d: %w", level, err)
	}

	return Puzzle{
		Level:               level,
		Title:               definition.Title,
		Letters:             definition.Letters,
		Words:               placed,
		SpecialEvent:        definition.SpecialEvent,
		CompletionBonus:     definition.CompletionBonus,
		CompletionBonusName: definition.CompletionBonusName,
	}, nil
}

type gridPoint struct {
	row int
	col int
}

type gridCell struct {
	letter rune
	across bool
	down   bool
}

func placeWords(words []string) ([]PlacedWord, error) {
	if len(words) == 0 {
		return nil, fmt.Errorf("puzzle has no words")
	}

	placed := []PlacedWord{{Answer: words[0], Row: 0, Col: 0, Direction: Across}}
	grid := map[gridPoint]gridCell{}
	writeWord(grid, placed[0])

	placed, ok := placeRemaining(words[1:], placed, grid)
	if !ok {
		return nil, fmt.Errorf("cannot build connected crossword")
	}

	normalize(placed)
	return placed, nil
}

func placeRemaining(words []string, placed []PlacedWord, grid map[gridPoint]gridCell) ([]PlacedWord, bool) {
	if len(words) == 0 {
		return placed, true
	}

	for wordIndex, answer := range words {
		for _, candidate := range findPlacements(answer, placed, grid) {
			nextGrid := cloneGrid(grid)
			writeWord(nextGrid, candidate)
			nextPlaced := append(append([]PlacedWord(nil), placed...), candidate)
			nextWords := append([]string(nil), words[:wordIndex]...)
			nextWords = append(nextWords, words[wordIndex+1:]...)
			if result, ok := placeRemaining(nextWords, nextPlaced, nextGrid); ok {
				return result, true
			}
		}
	}
	return nil, false
}

func findPlacements(answer string, placed []PlacedWord, grid map[gridPoint]gridCell) []PlacedWord {
	var candidates []PlacedWord
	seen := map[PlacedWord]bool{}
	for _, existing := range placed {
		for existingIndex, existingLetter := range existing.Answer {
			for answerIndex, answerLetter := range answer {
				if existingLetter != answerLetter {
					continue
				}

				candidate := PlacedWord{Answer: answer, Direction: Down}
				if existing.Direction == Across {
					candidate.Row = existing.Row - answerIndex
					candidate.Col = existing.Col + existingIndex
				} else {
					candidate.Direction = Across
					candidate.Row = existing.Row + existingIndex
					candidate.Col = existing.Col - answerIndex
				}

				if !seen[candidate] && placementFits(candidate, grid) {
					seen[candidate] = true
					candidates = append(candidates, candidate)
				}
			}
		}
	}
	return candidates
}

func cloneGrid(grid map[gridPoint]gridCell) map[gridPoint]gridCell {
	copy := make(map[gridPoint]gridCell, len(grid))
	for point, cell := range grid {
		copy[point] = cell
	}
	return copy
}

func placementFits(word PlacedWord, grid map[gridPoint]gridCell) bool {
	if _, occupied := grid[wordPoint(word, -1)]; occupied {
		return false
	}
	if _, occupied := grid[wordPoint(word, len(word.Answer))]; occupied {
		return false
	}

	overlaps := 0
	for index, letter := range word.Answer {
		point := wordPoint(word, index)
		if existing, ok := grid[point]; ok {
			if existing.letter != letter {
				return false
			}
			// A cell can hold one across and one down word, never two parallel words.
			if (word.Direction == Across && existing.across) || (word.Direction == Down && existing.down) {
				return false
			}
			overlaps++
			continue
		}

		// New cells need clearance on both sides of the word.
		if word.Direction == Across {
			if _, occupied := grid[gridPoint{row: point.row - 1, col: point.col}]; occupied {
				return false
			}
			if _, occupied := grid[gridPoint{row: point.row + 1, col: point.col}]; occupied {
				return false
			}
		} else {
			if _, occupied := grid[gridPoint{row: point.row, col: point.col - 1}]; occupied {
				return false
			}
			if _, occupied := grid[gridPoint{row: point.row, col: point.col + 1}]; occupied {
				return false
			}
		}
	}
	return overlaps > 0
}

func writeWord(grid map[gridPoint]gridCell, word PlacedWord) {
	for index, letter := range word.Answer {
		point := wordPoint(word, index)
		cell := grid[point]
		cell.letter = letter
		if word.Direction == Across {
			cell.across = true
		} else {
			cell.down = true
		}
		grid[point] = cell
	}
}

func wordPoint(word PlacedWord, index int) gridPoint {
	point := gridPoint{row: word.Row, col: word.Col}
	if word.Direction == Across {
		point.col += index
	} else {
		point.row += index
	}
	return point
}

func normalize(words []PlacedWord) {
	minRow, minCol := words[0].Row, words[0].Col
	for _, word := range words {
		minRow = min(minRow, word.Row)
		minCol = min(minCol, word.Col)
	}
	for index := range words {
		words[index].Row -= minRow
		words[index].Col -= minCol
		words[index].Answer = strings.ToUpper(words[index].Answer)
	}
}
