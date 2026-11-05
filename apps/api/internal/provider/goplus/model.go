package goplus

import "encoding/json"

// envelope is the live-verified GoPlus response shape:
// {code:int, message:string, result:{flag:"0"/"1" strings + data_source}}.
// result may be null or {} — both mean "no signals" (OK-empty, F8).
type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Result  json.RawMessage `json:"result"`
}
