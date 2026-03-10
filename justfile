# Bus build commands
# INBER_ENV is injected by bus-agent (0, 1, 2) or defaults to 0

env := env_var_or_default("INBER_ENV", "0")
base_port := if env == "0" { "9010" } else if env == "1" { "9110" } else { "9210" }

# Build bus binary
build:
  go build -o bus .

# Run bus on environment's port
dev: build
  ./bus -addr "127.0.0.1:{{base_port}}"

# Run tests
test:
  go test ./...

# Clean
clean:
  rm -f bus
