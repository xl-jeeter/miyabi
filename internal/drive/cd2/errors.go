package cd2

import (
	"errors"

	"github.com/ppxb/miyabi/internal/domain"
)

var (
	// ErrInvalidID reports a file ID that is not a path-derived adapter ID.
	ErrInvalidID = domain.E(domain.KindInvalid, "CloudDrive2 文件 ID 无效", nil)

	// ErrInvalidInfoHash reports a magnet URI without a usable BTIH hash.
	ErrInvalidInfoHash = domain.E(domain.KindInvalid, "磁力链接缺少有效的 info hash", nil)

	// ErrUnsupported reports a drive.Client method the adapter cannot serve.
	// CloudDrive2 manages its own cloud sign-in, so it has no QR login flow.
	ErrUnsupported = errors.New("clouddrive2 adapter does not support this operation")

	// ErrMissingEndpoint reports an adapter constructed without a gRPC address.
	ErrMissingEndpoint = domain.E(domain.KindInvalid, "未配置 CloudDrive2 接入地址", nil)

	// ErrTokenRequired reports a connection without an API token or credentials.
	ErrTokenRequired = domain.E(domain.KindUnauthorized, "未配置 CloudDrive2 API Token 或账号密码", nil)

	// ErrAccountRequired reports an ambiguous multi-account CloudDrive2 setup.
	ErrAccountRequired = domain.E(domain.KindInvalid, "CloudDrive2 存在多个云盘账号，请配置 MIYABI_CD2_ACCOUNT", nil)

	// ErrAccountNotFound reports a configured account CloudDrive2 does not hold.
	ErrAccountNotFound = domain.E(domain.KindNotFound, "CloudDrive2 未找到配置的云盘账号", nil)

	// ErrNotFound maps a CloudDrive2 NotFound status onto the pan sentinel
	// callers already use for missing files.
	ErrNotFound = domain.E(domain.KindNotFound, "CloudDrive2 中找不到该文件", nil)
)
