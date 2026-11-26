// Package gen holds the Go bindings generated from CloudDrive2's
// clouddrive.proto (v1.2.0). The generated files are committed so builds do not
// depend on protoc; regenerate them with:
//
//	protoc -I internal/drive/cd2/proto \
//	  --go_out=internal/drive/cd2/gen --go_opt=paths=source_relative \
//	  --go_opt=Mclouddrive.proto=github.com/ppxb/miyabi/internal/drive/cd2/gen \
//	  --go-grpc_out=internal/drive/cd2/gen --go-grpc_opt=paths=source_relative \
//	  --go-grpc_opt=Mclouddrive.proto=github.com/ppxb/miyabi/internal/drive/cd2/gen \
//	  internal/drive/cd2/proto/clouddrive.proto
package gen
