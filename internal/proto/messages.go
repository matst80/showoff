package proto

// Auth is sent by client to server as first line on control connection.
type Auth struct {
	Token  string `json:"token"`
	Name   string `json:"name"`
	Target string `json:"target"`
}

// AuthOK server -> client acknowledgement.
type AuthOK struct {
	Msg string `json:"msg"`
}

// Request server -> client asking to open a data connection for an incoming public connection.
type Request struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Data is the handshake on the data connection containing the request ID.
type Data struct {
	ID string `json:"id"`
}
