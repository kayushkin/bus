module github.com/kayushkin/bus

go 1.24.0

replace github.com/kayushkin/model-store => ../model-store

replace github.com/kayushkin/aiauth => ../aiauth

require github.com/nats-io/nats.go v1.49.0

require (
	github.com/klauspost/compress v1.18.2 // indirect
	github.com/nats-io/nkeys v0.4.12 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	golang.org/x/crypto v0.46.0 // indirect
)

require golang.org/x/sys v0.39.0 // indirect

replace github.com/kayushkin/forge => ../forge
