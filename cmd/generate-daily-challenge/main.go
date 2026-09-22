package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/mjones/temporal-word-game/internal/activities"
	"github.com/mjones/temporal-word-game/internal/bootstrap"
	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/mjones/temporal-word-game/internal/workflows"
)

const dateLayout = "2006-01-02"

type campaignFile struct {
	ID            string                 `json:"id"`
	Kind          campaign.Kind          `json:"kind"`
	SingleAttempt bool                   `json:"singleAttempt"`
	Title         string                 `json:"title"`
	Description   string                 `json:"description"`
	Game          campaign.GameSummary   `json:"game"`
	StartsAt      *time.Time             `json:"startsAt"`
	EndsAt        *time.Time             `json:"endsAt"`
	Unlock        campaignUnlockFile     `json:"unlock"`
	Requirements  campaign.Requirements  `json:"requirements"`
	Levels        []game.LevelDefinition `json:"levels"`
}

type campaignUnlockFile struct {
	InitialLevels     int `json:"initialLevels"`
	LevelsPerInterval int `json:"levelsPerInterval"`
	IntervalHours     int `json:"intervalHours"`
}

type fileList []string

func (files *fileList) String() string { return fmt.Sprint([]string(*files)) }

func (files *fileList) Set(value string) error {
	*files = append(*files, value)
	return nil
}

func main() {
	date := flag.String("date", "", "Toronto calendar date in YYYY-MM-DD format")
	var exclusionFiles fileList
	flag.Var(&exclusionFiles, "exclude-file", "campaign JSON whose answers must not be reused (repeatable)")
	flag.Parse()
	if *date == "" {
		log.Fatal("-date is required")
	}

	location, err := time.LoadLocation("America/Toronto")
	if err != nil {
		log.Fatal(err)
	}
	startsAt, err := time.ParseInLocation(dateLayout, *date, location)
	if err != nil || startsAt.Format(dateLayout) != *date {
		log.Fatalf("date must use YYYY-MM-DD: %q", *date)
	}

	excludedWords, err := readExcludedWords(exclusionFiles)
	if err != nil {
		log.Fatal(err)
	}

	input := workflows.GenerateDailyWordflowChallengeActivityInput{
		Date:          *date,
		StartsAt:      startsAt,
		EndsAt:        startsAt.AddDate(0, 0, 1),
		ExcludedWords: excludedWords,
	}
	var generated workflows.WordflowCampaignWorkflowInput
	for attempt := 1; attempt <= 5; attempt++ {
		generated, err = (&activities.DailyChallenges{}).GenerateDailyWordflowChallenge(context.Background(), input)
		if err == nil {
			break
		}
		log.Printf("generation attempt %d failed: %v", attempt, err)
	}
	if err != nil {
		log.Fatal(err)
	}

	encoded, err := json.MarshalIndent(toCampaignFile(generated), "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(encoded))
}

func readExcludedWords(paths []string) ([]string, error) {
	var words []string
	seen := map[string]bool{}
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read exclusion campaign %q: %w", path, err)
		}
		input, err := bootstrap.ParseWordflowCampaign(contents)
		if err != nil {
			return nil, fmt.Errorf("parse exclusion campaign %q: %w", path, err)
		}
		for _, level := range input.Levels {
			for _, placed := range level.Words {
				if !seen[placed.Answer] {
					seen[placed.Answer] = true
					words = append(words, placed.Answer)
				}
			}
		}
	}
	return words, nil
}

func toCampaignFile(input workflows.WordflowCampaignWorkflowInput) campaignFile {
	levels := make([]game.LevelDefinition, 0, len(input.Levels))
	for _, puzzle := range input.Levels {
		words := make([]string, 0, len(puzzle.Words))
		for _, placed := range puzzle.Words {
			words = append(words, placed.Answer)
		}
		levels = append(levels, game.LevelDefinition{
			Title:            puzzle.Title,
			Letters:          puzzle.Letters,
			Words:            words,
			TimeLimitSeconds: puzzle.TimeLimitSeconds,
			BasePoints:       puzzle.BasePoints,
			HintPrices:       puzzle.HintPrices,
		})
	}
	return campaignFile{
		ID:            input.Definition.ID,
		Kind:          input.Definition.Kind,
		SingleAttempt: input.Definition.SingleAttempt,
		Title:         input.Definition.Title,
		Description:   input.Definition.Description,
		Game:          input.Definition.Game,
		StartsAt:      input.Definition.StartsAt,
		EndsAt:        input.Definition.EndsAt,
		Unlock: campaignUnlockFile{
			InitialLevels:     input.Definition.Unlock.InitialLevels,
			LevelsPerInterval: input.Definition.Unlock.LevelsPerInterval,
			IntervalHours:     int(input.Definition.Unlock.Interval / time.Hour),
		},
		Requirements: input.Definition.Requirements,
		Levels:       levels,
	}
}
