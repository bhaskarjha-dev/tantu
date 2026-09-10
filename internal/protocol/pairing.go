package protocol

// PairHelloPayload represents the payload of a pair_hello protocol message.
type PairHelloPayload struct {
	CertPEM    []byte `json:"cert_pem"`
	Name       string `json:"name,omitempty"`
	ListenPort int    `json:"listen_port,omitempty"`
}

// PairDecisionPayload represents the payload of a pair_decision protocol message.
type PairDecisionPayload struct {
	Accept bool   `json:"accept"`
	Reason string `json:"reason,omitempty"`
}
