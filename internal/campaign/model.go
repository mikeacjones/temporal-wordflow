package campaign

import "time"

const GameWordflow = "wordflow"

type Kind string

const KindDailyChallenge Kind = "daily-challenge"

type Status string

const (
	StatusUpcoming Status = "upcoming"
	StatusActive   Status = "active"
	StatusEnded    Status = "ended"
)

type LevelStatus string

const (
	LevelLocked    LevelStatus = "locked"
	LevelUnlocked  LevelStatus = "unlocked"
	LevelAvailable LevelStatus = "available"
	LevelActive    LevelStatus = "active"
	LevelComplete  LevelStatus = "complete"
)

type GameSummary struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type Registration struct {
	CampaignID string      `json:"campaignId"`
	WorkflowID string      `json:"workflowId"`
	Game       GameSummary `json:"game"`
}

type CatalogView struct {
	Games     []GameSummary  `json:"games"`
	Campaigns []Registration `json:"campaigns"`
}

type RequiredLevel struct {
	CampaignID string `json:"campaignId"`
	Level      int    `json:"level"`
}

type Requirements struct {
	Campaigns []string        `json:"campaigns,omitempty"`
	Levels    []RequiredLevel `json:"levels,omitempty"`
}

type UnlockPolicy struct {
	InitialLevels     int           `json:"initialLevels"`
	LevelsPerInterval int           `json:"levelsPerInterval"`
	Interval          time.Duration `json:"interval"`
}

type Definition struct {
	ID            string       `json:"id"`
	Kind          Kind         `json:"kind,omitempty"`
	SingleAttempt bool         `json:"singleAttempt,omitempty"`
	Game          GameSummary  `json:"game"`
	Title         string       `json:"title"`
	Description   string       `json:"description"`
	StartsAt      *time.Time   `json:"startsAt,omitempty"`
	EndsAt        *time.Time   `json:"endsAt,omitempty"`
	Requirements  Requirements `json:"requirements"`
	Unlock        UnlockPolicy `json:"unlock"`
}

type Summary struct {
	CampaignID  string      `json:"campaignId"`
	Kind        Kind        `json:"kind,omitempty"`
	WorkflowID  string      `json:"workflowId"`
	Game        GameSummary `json:"game"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Status      Status      `json:"status"`
	StartsAt    *time.Time  `json:"startsAt,omitempty"`
	EndsAt      *time.Time  `json:"endsAt,omitempty"`
	TotalLevels int         `json:"totalLevels"`
}

type PlayerCampaignProgress struct {
	CampaignID      string    `json:"campaignId"`
	JoinedAt        time.Time `json:"joinedAt"`
	NextLevel       int       `json:"nextLevel"`
	TotalLevels     int       `json:"totalLevels"`
	Completed       bool      `json:"completed"`
	Failed          bool      `json:"failed,omitempty"`
	CompletedLevels int       `json:"completedLevels"`
}

type PlayerProgress struct {
	Campaigns        []PlayerCampaignProgress `json:"campaigns"`
	ActiveCampaignID string                   `json:"activeCampaignId,omitempty"`
	ActiveLevel      int                      `json:"activeLevel,omitempty"`
}

type QueryInput struct {
	Player PlayerProgress `json:"player"`
}

type LevelView struct {
	Level      int         `json:"level"`
	Title      string      `json:"title"`
	Status     LevelStatus `json:"status"`
	UnlockedAt *time.Time  `json:"unlockedAt,omitempty"`
}

type View struct {
	CampaignID      string      `json:"campaignId"`
	Kind            Kind        `json:"kind,omitempty"`
	WorkflowID      string      `json:"workflowId"`
	Game            GameSummary `json:"game"`
	Title           string      `json:"title"`
	Description     string      `json:"description"`
	Status          Status      `json:"status"`
	StartsAt        *time.Time  `json:"startsAt,omitempty"`
	EndsAt          *time.Time  `json:"endsAt,omitempty"`
	Eligible        bool        `json:"eligible"`
	Failed          bool        `json:"failed,omitempty"`
	LockedReason    string      `json:"lockedReason,omitempty"`
	NextLevel       int         `json:"nextLevel"`
	CompletedLevels int         `json:"completedLevels"`
	TotalLevels     int         `json:"totalLevels"`
	Levels          []LevelView `json:"levels"`
}

type PointAward struct {
	Description string `json:"description"`
	Points      int    `json:"points"`
}

// LevelResult is the small, game-independent result returned to a Player Workflow.
type LevelResult struct {
	GameID      string       `json:"gameId"`
	CampaignID  string       `json:"campaignId"`
	Level       int          `json:"level"`
	Attempts    int          `json:"attempts"`
	CompletedAt time.Time    `json:"completedAt"`
	TimedOut    bool         `json:"timedOut,omitempty"`
	Awards      []PointAward `json:"awards"`
}
