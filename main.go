// Copyright 2018 Google Inc. All Rights Reserved.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/secretsmanager"
	"github.com/hashicorp/vault/api"
	"github.com/pkg/errors"
	"github.com/spf13/viper"
)

var (
	secretsManagerSecretID string
	client                 *api.Client
)

func init() {
	viper.AutomaticEnv()

	viper.SetDefault("check_interval", 10*time.Second)
	viper.SetDefault("vault_secret_shares", 5)
	viper.SetDefault("vault_secret_threshold", 3)
	viper.SetDefault("log_level", slog.LevelInfo)

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.Level(viper.GetInt("log_level")),
	})))
}

func main() {
	slog.Info("Starting up...")

	secretsManagerSecretID = viper.GetString("secretsmanager_secret_id")
	if secretsManagerSecretID == "" {
		log.Fatal("SECRETSMANAGER_SECRET_ID env is required")
	}

	ctx := context.Background()

	checkIAMPermissionsForSecret(ctx)

	var err error
	client, err = newVaultAPIClient()
	if err != nil {
		log.Fatalf("Create vault API client: %v", err)
	}

	ticker := time.NewTicker(viper.GetDuration("check_interval"))

	if err := checkVaultStatus(ctx); err != nil {
		slog.Error("Checking vault for the first time", "error", err)
	}

	for {
		slog.Debug("Tick", "time", <-ticker.C)
		if err := checkVaultStatus(ctx); err != nil {
			slog.Error("Checking vault", "error", err)
		}
	}
}

func checkIAMPermissionsForSecret(ctx context.Context) {
	var (
		sm           = secretsmanager.New(session.Must(session.NewSession()))
		secretString = fmt.Sprintf("%d", time.Now().UnixNano())
	)

	_, err := sm.UpdateSecretWithContext(ctx, &secretsmanager.UpdateSecretInput{
		SecretId:     &secretsManagerSecretID,
		SecretString: &secretString,
	})
	if err != nil {
		log.Fatalf("Update secret with id %q: %v", secretsManagerSecretID, err)
	}

	_, err = sm.GetSecretValueWithContext(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: &secretsManagerSecretID,
	})
	if err != nil {
		log.Fatalf("Get secret with id %q: %v", secretsManagerSecretID, err)
	}
}

func newVaultAPIClient() (*api.Client, error) {
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

func checkVaultStatus(ctx context.Context) error {
	slog.Debug("Checking vault status")

	healthResponse, err := client.Sys().Health()
	if err != nil {
		return errors.Wrap(err, "read health")
	}

	slog.Debug("Got vault status", "data", healthResponse)

	if healthResponse.Initialized && !healthResponse.Sealed {
		slog.Debug("Nothing to do")
		return nil
	}

	sm := secretsmanager.New(session.Must(session.NewSession()))

	if !healthResponse.Initialized {
		var (
			hostname = os.Getenv("HOSTNAME")
			replica  = int(hostname[len(hostname)-1]) - 48
		)

		slog.Debug("Vault replica", "n", replica)

		switch replica {
		case 0:
			err = initialize(ctx, sm)
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
		err = unseal(ctx, sm)
		if err != nil {
			return errors.Wrap(err, "unseal")
		}
	}

	return nil
}

func initialize(ctx context.Context, sm *secretsmanager.SecretsManager) error {
	slog.Info("Initializing vault server...")

	initResponse, err := client.Sys().InitWithContext(ctx, &api.InitRequest{
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
		output, err := sm.UpdateSecretWithContext(ctx, &secretsmanager.UpdateSecretInput{
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

func joinRaftCluster(ctx context.Context) error {
	slog.Info("Joining RAFT cluster...")

	opts := api.RaftJoinRequest{
		LeaderAPIAddr:    viper.GetString("raft_leader_api_addr"),
		LeaderCACert:     parseEnvFile(viper.GetString("raft_leader_ca_cert")),
		LeaderClientCert: parseEnvFile(viper.GetString("raft_leader_client_cert")),
		LeaderClientKey:  parseEnvFile(viper.GetString("raft_leader_client_key")),
	}

	res, err := client.Sys().RaftJoinWithContext(ctx, &opts)
	if err != nil {
		return err
	}
	if !res.Joined {
		return errors.Errorf("couldn't join with opts: %#v", opts)
	}

	slog.Info("Joined RAFT cluster successfully")
	return nil
}

func unseal(ctx context.Context, sm *secretsmanager.SecretsManager) error {
	slog.Info("Fetching unseal keys...", "secretID", secretsManagerSecretID)

	secret, err := sm.GetSecretValueWithContext(ctx, &secretsmanager.GetSecretValueInput{
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
		status, err := client.Sys().UnsealWithContext(ctx, key)
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
