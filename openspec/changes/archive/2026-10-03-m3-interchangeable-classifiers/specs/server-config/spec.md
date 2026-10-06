# Spec Delta

## ADDED Requirements

### Requirement: Secrets are references
A secret SHALL be configured only as the name of an environment variable (`*_env`) or the path of a protected file (`*_file`), never inline. `check-config` SHALL show each reference and whether it resolves, never its value. The server SHALL refuse to start when an enabled profile's secret reference does not resolve, or when a secret file is readable by other users.

#### Scenario: Secret reference redacted
- **WHEN** `check-config` runs with a profile whose `api_key_env` names a variable that is set
- **THEN** the output shows the variable's name and that it is set, and the secret value appears nowhere in the output or the logs

#### Scenario: Missing secret
- **WHEN** an enabled profile's `api_key_env` names a variable that is not set
- **THEN** startup fails with an error naming the profile and the variable

### Requirement: Classifier configuration is validated
The server SHALL refuse to start when:
- a profile names an unknown adapter, or an endpoint that is not an absolute `http` or `https` URL;
- a `cloud` profile uses plain `http`;
- a policy names an unknown profile, a profile that does not support its task, or more than two fallbacks;
- a priced profile is configured while the daily or per-job cap is zero.

Each error SHALL name the offending key.

#### Scenario: Metered profile without caps
- **WHEN** a profile has an input price and `classifier.daily_cap` is zero
- **THEN** startup fails with an error naming the profile and `classifier.daily_cap`

#### Scenario: Plain HTTP to a cloud endpoint
- **WHEN** a profile with locality `cloud` has endpoint `http://api.example.com/v1`
- **THEN** startup fails with an error naming the profile's `endpoint`
