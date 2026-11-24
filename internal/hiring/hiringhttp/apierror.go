package hiringhttp

// APIError maps a hiring service or store error to the API error the HTTP routes answer with. The gRPC server
// uses it so both transports speak about errors the same way.
func APIError(err error) error { return hiringError(err) }
