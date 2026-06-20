package auth

import (
	"net/http"
	"testing"
)

func TestIsRequestInvalidError_RequestShapeSignatures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			// Real upstream body; note no HTTP status is attached, so the fix must
			// match on the code token alone rather than relying on status==400.
			name: "missing_required_parameter without status",
			err:  &Error{Message: `{"error":{"message":"Missing required parameter: 'input[5].name'.","type":"invalid_request_error","param":"input[5].name","code":"missing_required_parameter"}}`},
			want: true,
		},
		{
			name: "invalid_value without status",
			err:  &Error{Message: `{"error":{"message":"Invalid 'input[0].content[1].image_url'. Expected a valid URL, but got a value with an invalid format.","type":"invalid_request_error","param":"input[0].content[1].image_url","code":"invalid_value"}}`},
			want: true,
		},
		{
			// Different shape: {"detail":"..."} with no type/code and no status.
			name: "unsupported parameter detail without status",
			err:  &Error{Message: `{"detail":"Unsupported parameter: enable_thinking"}`},
			want: true,
		},
		{
			// Control: a transient failure with no status and none of the request
			// signatures must remain retryable (credential rotation still allowed).
			name: "generic transient error stays retryable",
			err:  &Error{Message: "connection reset by peer"},
			want: false,
		},
		{
			// Control: model-support errors must keep falling through to another
			// auth/upstream and not be swallowed by the new token match.
			name: "model support error is not request-invalid",
			err:  &Error{HTTPStatus: http.StatusBadRequest, Message: "invalid_request_error: The requested model is not supported."},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRequestInvalidError(tc.err); got != tc.want {
				t.Fatalf("isRequestInvalidError = %v, want %v", got, tc.want)
			}
		})
	}
}
