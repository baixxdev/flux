package filter

// 全局Filter排序
const (
	OrderFilterJwtVerification    = -90
	OrderFilterEndpointPermission = -80
)

const (
	ConfigKeyCacheExpiration  = "cache-expiration"
	ConfigKeyCacheSize        = "cache-size"
	ConfigKeyDisabled         = "disabled"
	UpstreamConfigKeyProtocol = "upstream-protocol"
	UpstreamConfigKeyHost     = "upstream-host"
	UpstreamConfigKeyUri      = "upstream-uri"
	UpstreamConfigKeyMethod   = "upstream-method"
	JwtConfigKeySubject       = "jwt-subject-key"
	JwtConfigKeyIssuer        = "jwt-issuer-key"
	JwtConfigKeyLookupToken   = "jwt-lookup-token"
)

const (
	KeyScopedValueJwtClaims = "jwt-claims"
)

const (
	defValueCacheExpiration = 30
	defValueCacheSize       = 1_0000
)
