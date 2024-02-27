// Copyright 2018 Google Inc. All Rights Reserved.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package main

import (
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/hashicorp/vault/api"
	"github.com/pkg/errors"
	"github.com/spf13/viper"
)

var (
	secretsManagerSecretID string
	vaultClient            *api.Client
	secretsManagerClient   *secretsmanager.Client
)

func init() {
	// Viper configuration
	viper.AutomaticEnv()
	viper.SetDefault("check_interval", 10*time.Second)
	viper.SetDefault("vault_secret_shares", 5)
	viper.SetDefault("vault_secret_threshold", 3)
	viper.SetDefault("log_level", slog.LevelInfo)

	// Logging configuration
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.Level(viper.GetInt("log_level")),
	})))

	// Read required environment variables
	secretsManagerSecretID = viper.GetString("secretsmanager_secret_id")
	if secretsManagerSecretID == "" {
		log.Fatal("SECRETSMANAGER_SECRET_ID env is required")
	}
}

func main() {
	var (
		ctx = context.Background()
		err error
	)

	slog.Info("Starting up...")

	slog.Debug("Creating AWS Secrets Manager client...")
	secretsManagerClient, err = newAWSSecretManagerClient(ctx)
	if err != nil {
		log.Fatalf("Create AWS Secret Manager client: %v", err)
	}

	slog.Debug("Checking the secret exists", "secretID", secretsManagerSecretID)
	if err = checkSecretExistence(ctx); err != nil {
		log.Fatalf("Checking secret existence: %v", err)
	}

	slog.Debug("Creating HashiCorp Vault cient...")
	vaultClient, err = newHashiCorpVaultClient()
	if err != nil {
		log.Fatalf("Create HashiCorp Vault client: %v", err)
	}

	slog.Debug("Starting Vault check routine...")
	ticker := time.NewTicker(viper.GetDuration("check_interval"))

	if err := checkVaultStatus(ctx); err != nil {
		slog.Error("Checking Vault for the first time", "error", err)
	}

	for {
		slog.Debug("Tick", "time", <-ticker.C)
		if err := checkVaultStatus(ctx); err != nil {
			slog.Error("Checking Vault", "error", err)
		}
	}
}

// Create SDK client for AWS Secrets Manager service.
// The SDK client can be configured using environment variables. See:
// - https://aws.github.io/aws-sdk-go-v2/docs/configuring-sdk
// - https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/config#EnvConfig
func newAWSSecretManagerClient(ctx context.Context) (*secretsmanager.Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "load SDK config")
	}

	return secretsmanager.NewFromConfig(cfg), nil
}

// Create API client for HashiCorp Vault.
// The HashiCorp Vault API client can be configured using environment variables. See:
// - https://developer.hashicorp.com/vault/docs/commands#environment-variables
// - https://pkg.go.dev/github.com/hashicorp/vault/api#Config.ReadEnvironment
func newHashiCorpVaultClient() (*api.Client, error) {
	config := api.DefaultConfig()

	if err := config.ReadEnvironment(); err != nil {
		return nil, errors.Wrap(err, "failed to read environment")
	}

	client, err := api.NewClient(config)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create client")
	}

	return client, nil
}

func checkSecretExistence(ctx context.Context) error {
	secret, err := secretsManagerClient.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
		SecretId: &secretsManagerSecretID,
	})
	if err != nil {
		return errors.Wrap(err, "describe secret")
	}

	slog.Debug("Secret exists", "arn", aws.ToString(secret.ARN))
	return nil
}

// Check vault health status and initialize, join Raft cluster and unseal as needed.
func checkVaultStatus(ctx context.Context) error {
	slog.Debug("Checking vault status")

	healthResponse, err := vaultClient.Sys().Health()
	if err != nil {
		return errors.Wrap(err, "read health")
	}

	slog.Debug("Got vault status", "data", healthResponse)

	if healthResponse.Initialized && !healthResponse.Sealed {
		slog.Debug("Nothing to do")
		return nil
	}

	if !healthResponse.Initialized {
		var (
			hostname = os.Getenv("HOSTNAME")
			replica  = int(hostname[len(hostname)-1]) - 48
		)

		slog.Debug("Vault replica", "n", replica)

		switch replica {
		case 0:
			err = initialize(ctx)
			if err != nil {
				return errors.Wrap(err, "initialize")
			}

		default:
			err = joinRaftCluster(ctx)
			if err != nil {
				return errors.Wrap(err, "raft join")
			}
		}
	}

	if healthResponse.Sealed {
		err = unseal(ctx)
		if err != nil {
			return errors.Wrap(err, "unseal")
		}
	}

	return nil
}

// Initialize vault server and save generated keys in AWS Secrets Manager secret.
// The initialization process is just executed for the first replica of the statefulset,
// where the hostname ends with a 0.
func initialize(ctx context.Context) error {
	slog.Info("Initializing vault server...")

	initResponse, err := vaultClient.Sys().InitWithContext(ctx, &api.InitRequest{
		SecretShares:    viper.GetInt("vault_secret_shares"),
		SecretThreshold: viper.GetInt("vault_secret_threshold"),
	})
	if err != nil {
		return errors.Wrap(err, "init vault")
	}

	slog.Info("Vault server initialized successfully, uploading result to AWS...", "secretID", secretsManagerSecretID)

	data, err := json.Marshal(&initResponse)
	if err != nil {
		panic("couldn't marshal init response:" + err.Error())
	}

	secretString := string(data)

	for {
		output, err := secretsManagerClient.UpdateSecret(ctx, &secretsmanager.UpdateSecretInput{
			SecretId:     &secretsManagerSecretID,
			SecretString: &secretString,
		})
		if err == nil {
			slog.Info("Updated secret", "arn", *output.ARN, "version", *output.VersionId)
			break
		}
		slog.Error("Cannot update secret", "error", err)
		time.Sleep(3 * time.Second)
	}

	slog.Info("Initialization process completed")
	return nil
}

// Join Raft cluster contacting leader, used to bootstrap follower replicas.
func joinRaftCluster(ctx context.Context) error {
	slog.Info("Joining RAFT cluster...")

	opts := api.RaftJoinRequest{
		LeaderAPIAddr:    viper.GetString("raft_leader_api_addr"),
		LeaderCACert:     parseEnvFile(viper.GetString("raft_leader_ca_cert")),
		LeaderClientCert: parseEnvFile(viper.GetString("raft_leader_client_cert")),
		LeaderClientKey:  parseEnvFile(viper.GetString("raft_leader_client_key")),
	}

	res, err := vaultClient.Sys().RaftJoinWithContext(ctx, &opts)
	if err != nil {
		return err
	}
	if !res.Joined {
		return errors.Errorf("couldn't join with opts: %#v", opts)
	}

	slog.Info("Joined RAFT cluster successfully")
	return nil
}

// Fetch unseal keys from AWS Secrets Manager secret and unseal Vault server.
func unseal(ctx context.Context) error {
	slog.Info("Fetching unseal keys...", "secretID", secretsManagerSecretID)

	secret, err := secretsManagerClient.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: &secretsManagerSecretID,
	})
	if err != nil {
		return errors.Wrap(err, "get AWS secret")
	}

	var initResponse api.InitResponse

	err = json.Unmarshal([]byte(*secret.SecretString), &initResponse)
	if err != nil {
		return errors.Wrap(err, "unmarshal")
	}

	slog.Info("Unseal keys received, unsealing vault server...")

	for i, key := range initResponse.KeysB64 {
		status, err := vaultClient.Sys().UnsealWithContext(ctx, key)
		if err != nil {
			return errors.Wrapf(err, "unseal shard %d", i)
		}
		slog.Info("Unseal", "progress", status.Progress)
		if status.Progress <= 0 {
			break
		}
	}

	slog.Info("Vault server unsealed successfully")
	return nil
}

// Returns file contents if raw string is in format `@<file-path>`.
func parseEnvFile(raw string) string {
	if len(raw) == 0 || raw[0] != '@' {
		return raw
	}

	contents, err := os.ReadFile(raw[1:])
	if err != nil {
		panic(err)
	}
	return string(contents)
}
