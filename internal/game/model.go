package game

import (
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
)

type HintType string

const (
	HintLetter HintType = "letter"
	HintBrush  HintType = "brush"
	HintWord   HintType = "word"
)

type Direction string

const MaxWordflowLetters = 8

const (
	DefaultBasePoints          = 10
	DefaultLetterHintPointCost = 10
	DefaultBrushHintPointCost  = 20
	DefaultWordHintPointCost   = 30
)

const (
	Across Direction = "across"
	Down   Direction = "down"
)

type HintInventory struct {
	Letters int `json:"letters"`
	Brushes int `json:"brushes"`
	Words   int `json:"words"`
}

type HintPrices struct {
	Letter int `json:"letter"`
	Brush  int `json:"brush"`
	Word   int `json:"word"`
}

type Position struct {
	Row int `json:"row"`
	Col int `json:"col"`
}

type PlacedWord struct {
	Answer    string    `json:"answer"`
	Row       int       `json:"row"`
	Col       int       `json:"col"`
	Direction Direction `json:"direction"`
}

type SpecialEvent struct {
	Name        string `json:"name"`
	BonusPoints int    `json:"bonusPoints"`
}

// Puzzle is immutable input to a WordflowLevelWorkflow. Existing levels never depend on
// a future catalog deployment to reconstruct their answers.
type Puzzle struct {
	Level               int           `json:"level"`
	Title               string        `json:"title"`
	Letters             string        `json:"letters"`
	Words               []PlacedWord  `json:"words"`
	SpecialEvent        *SpecialEvent `json:"specialEvent,omitempty"`
	CompletionBonus     int           `json:"completionBonus,omitempty"`
	CompletionBonusName string        `json:"completionBonusName,omitempty"`
	TimeLimitSeconds    int           `json:"timeLimitSeconds,omitempty"`
	BasePoints          int           `json:"basePoints,omitempty"`
	HintPrices          HintPrices    `json:"hintPrices,omitempty"`
}

func (p Puzzle) EffectiveBasePoints() int {
	if p.BasePoints > 0 {
		return p.BasePoints
	}
	return DefaultBasePoints
}

func (p Puzzle) EffectiveHintPrices() HintPrices {
	prices := p.HintPrices
	if prices.Letter <= 0 {
		prices.Letter = DefaultLetterHintPointCost
	}
	if prices.Brush <= 0 {
		prices.Brush = DefaultBrushHintPointCost
	}
	if prices.Word <= 0 {
		prices.Word = DefaultWordHintPointCost
	}
	return prices
}

type CellView struct {
	Row      int    `json:"row"`
	Col      int    `json:"col"`
	Letter   string `json:"letter,omitempty"`
	Revealed bool   `json:"revealed"`
	Hinted   bool   `json:"hinted"`
	Missed   bool   `json:"missed,omitempty"`
}

type SolutionWordView struct {
	Answer string `json:"answer"`
	Found  bool   `json:"found"`
}

type WordView struct {
	Row       int        `json:"row"`
	Col       int        `json:"col"`
	Direction Direction  `json:"direction"`
	Length    int        `json:"length"`
	Found     bool       `json:"found"`
	Cells     []Position `json:"cells"`
}

type SpeedBonusTier struct {
	Points int       `json:"points"`
	EndsAt time.Time `json:"endsAt"`
}

type GameView struct {
	WorkflowID    string             `json:"workflowId"`
	PlayerID      string             `json:"playerId"`
	CampaignID    string             `json:"campaignId"`
	Level         int                `json:"level"`
	Title         string             `json:"title"`
	Letters       string             `json:"letters"`
	Cells         []CellView         `json:"cells"`
	Words         []WordView         `json:"words"`
	FoundWords    int                `json:"foundWords"`
	TotalWords    int                `json:"totalWords"`
	Attempts      int                `json:"attempts"`
	RejectedWords []string           `json:"rejectedWords"`
	SpeedBonuses  []SpeedBonusTier   `json:"speedBonuses"`
	HintBonus     int                `json:"hintBonus"`
	AccuracyBonus int                `json:"accuracyBonus"`
	Hints         HintInventory      `json:"hints"`
	HintPrices    HintPrices         `json:"hintPrices"`
	Complete      bool               `json:"complete"`
	TimedOut      bool               `json:"timedOut,omitempty"`
	ExpiresAt     *time.Time         `json:"expiresAt,omitempty"`
	SpecialEvent  *SpecialEvent      `json:"specialEvent,omitempty"`
	CompletedAt   *time.Time         `json:"completedAt,omitempty"`
	Score         *GameScore         `json:"score,omitempty"`
	SolutionWords []SolutionWordView `json:"solutionWords,omitempty"`
}

type GameScore struct {
	Points           int   `json:"points"`
	BasePoints       int   `json:"basePoints"`
	SpeedBonus       int   `json:"speedBonus"`
	AccuracyBonus    int   `json:"accuracyBonus"`
	HintBonus        int   `json:"hintBonus"`
	DurationSeconds  int64 `json:"durationSeconds"`
	IncorrectGuesses int   `json:"incorrectGuesses"`
	HintsUsed        int   `json:"hintsUsed"`
}

type GuessResult struct {
	Outcome string   `json:"outcome"`
	Game    GameView `json:"game"`
}

type HintResult struct {
	Outcome         string   `json:"outcome"`
	Game            GameView `json:"game"`
	PointsSpent     int      `json:"pointsSpent,omitempty"`
	PointsRemaining *int     `json:"pointsRemaining,omitempty"`
}

type ActiveGame struct {
	WorkflowID  string `json:"workflowId"`
	CampaignID  string `json:"campaignId"`
	Level       int    `json:"level"`
	Title       string `json:"title"`
	TotalLevels int    `json:"totalLevels"`
}

type LevelCompletion struct {
	CampaignID  string    `json:"campaignId"`
	Level       int       `json:"level"`
	Attempts    int       `json:"attempts"`
	CompletedAt time.Time `json:"completedAt"`
	Points      int       `json:"points"`
}

type Reward struct {
	CampaignID  string    `json:"campaignId"`
	Level       int       `json:"level"`
	Description string    `json:"description"`
	Points      int       `json:"points"`
	AwardedAt   time.Time `json:"awardedAt"`
}

type PlayerView struct {
	PlayerID             string                            `json:"playerId"`
	DisplayName          string                            `json:"displayName"`
	CreatedAt            time.Time                         `json:"createdAt"`
	LastSeenAt           time.Time                         `json:"lastSeenAt"`
	CurrentStreak        int                               `json:"currentStreak"`
	BestStreak           int                               `json:"bestStreak"`
	Points               int                               `json:"points"`
	LifetimePointsEarned int                               `json:"lifetimePointsEarned"`
	StreakFreeze         bool                              `json:"streakFreeze"`
	StreakFreezeCost     int                               `json:"streakFreezeCost"`
	Campaigns            []campaign.PlayerCampaignProgress `json:"campaigns"`
	CompletedLevelCount  int                               `json:"completedLevelCount"`
	CompletedLevels      []LevelCompletion                 `json:"completedLevels"`
	Rewards              []Reward                          `json:"rewards"`
	ActiveGame           *ActiveGame                       `json:"activeGame,omitempty"`
}

type LeaderboardEntry struct {
	PlayerID             string `json:"playerId"`
	DisplayName          string `json:"displayName"`
	LifetimePointsEarned int    `json:"lifetimePointsEarned"`
	CompletedLevels      int    `json:"completedLevels"`
	Version              int    `json:"version"`
}

type LeaderboardRank struct {
	DisplayName          string `json:"displayName"`
	LifetimePointsEarned int    `json:"lifetimePointsEarned"`
	CompletedLevels      int    `json:"completedLevels"`
	Rank                 int    `json:"rank"`
}

type LeaderboardView struct {
	Entries []LeaderboardRank `json:"entries"`
}
