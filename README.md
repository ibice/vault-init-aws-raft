# vault-init-aws-raft

This is a modified version of [vault-init-aws](https://github.com/caquino/vault-init-aws/) to store the Vault secret configuration in an AWS Secrets Manager secret.
The project has also been moderized to use Go modules and [aws-sdk-go-v2](https://aws.github.io/aws-sdk-go-v2/docs/) and support Vault running in [Raft mode](https://developer.hashicorp.com/vault/docs/configuration/storage/raft).

The `vault-init` service automates the process of [initializing](https://www.vaultproject.io/docs/commands/operator/init.html) and [unsealing](https://www.vaultproject.io/docs/concepts/seal.html#unsealing) HashiCorp Vault instances running on [Amazon Web Services](http://aws.amazon.com/).

After `vault-init` initializes a Vault server it stores master keys and root tokens, encrypted using [AWS Key Management Service](https://aws.amazon.com/kms/), to a user defined [Amazon S3](https://aws.amazon.com/s3/) bucket.

## Usage

The `vault-init` service is designed to be run alongside a Vault server and communicate over local host.

See the [example Terraform project](example/) for a complete example including required IAM policies.

## Configuration

The vault-init service supports the following environment variables for configuration:

- `LOG_LEVEL`: application log level. Set to -4 to see debug messages.
- `SECRETSMANAGER_SECRET_ID`: AWS Secrets Manager secret ARN to store information. It must exist, the application does not create it automatically.
- `CHECK_INTERVAL`: interval between status check requests to Vault (with [units](https://pkg.go.dev/time#ParseDuration)). Defaults to `10s`.
- `VAULT_SECRET_SHARES`: vault secret shares for initialization, defaults to 5. 
- `VAULT_SECRET_THRESHOLD`: vault secret threshold for unsealing, defaults to 3. 
- `RAFT_LEADER_API_ADDR`: URL of the Vault leader to bootstrap Raft followers (e.g. `http://vault-0.vault.svc`).
- `RAFT_LEADER_CA_CERT`: Raft leader CA cert if TLS is used. To read from a file, use the format `@<file-path>`.
- `RAFT_LEADER_CLIENT_CERT`: Raft leader client cert if TLS is used. To read from a file, use the format `@<file-path>`.
- `RAFT_LEADER_CLIENT_KEY`: Raft leader client key if TLS is used. To read from a file, use the format `@<file-path>`.

The AWS SDK client can be configured using environment variables. See:
- https://aws.github.io/aws-sdk-go-v2/docs/configuring-sdk
- https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/config#EnvConfig

The HashiCorp Vault API client can be configured using environment variables. See:
- https://developer.hashicorp.com/vault/docs/commands#environment-variables
- https://pkg.go.dev/github.com/hashicorp/vault/api#Config.ReadEnvironment
