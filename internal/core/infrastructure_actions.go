package core

// InfrastructureActionRequest keeps immutable machine mutations encrypted across
// controller restarts, independently from the original allocation request.
type InfrastructureActionRequest struct {
	OperationID      string
	ProviderRevision int64
	ManifestDigest   string
	EncryptedRequest string
	CipherDigest     string
}
