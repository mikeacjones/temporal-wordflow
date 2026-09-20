package temporalconfig

import (
	"context"
	"errors"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// LoadAPIKey leaves local environment configuration alone and resolves the
// deployed credential from Secrets Manager only when needed.
func LoadAPIKey(ctx context.Context) error {
	if os.Getenv("TEMPORAL_API_KEY") != "" {
		return nil
	}
	secretID := os.Getenv("TEMPORAL_API_KEY_SECRET_ID")
	if secretID == "" {
		return nil
	}

	awsConfig, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return err
	}
	secret, err := secretsmanager.NewFromConfig(awsConfig).GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: &secretID,
	})
	if err != nil {
		return err
	}
	if secret.SecretString == nil || *secret.SecretString == "" {
		return errors.New("Temporal API key secret is empty")
	}
	return os.Setenv("TEMPORAL_API_KEY", *secret.SecretString)
}
