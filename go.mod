module github.com/bmcpi/esp32-p4-kvm

go 1.26.2

require tinygo.org/x/drivers v0.35.0

require (
	github.com/tinywasm/fmt v0.23.7
	tinygo.org/x/tinyfs v0.5.0
)

require github.com/aperturerobotics/protobuf-go-lite v0.13.0

require (
	github.com/aperturerobotics/json-iterator-lite v1.1.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

ignore bundling/

tool github.com/aperturerobotics/protobuf-go-lite/cmd/protoc-gen-go-lite
