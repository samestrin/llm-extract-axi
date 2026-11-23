package llmapi

import (
	"errors"
	"net/url"
	"strings"
)

// unparsableURL stands in for a URL that does not parse: the secret in it
// cannot be found to hide, so none of it is shown.
const unparsableURL = "(base URL not shown: it does not parse)"

// secretParamWords mark a query parameter whose value is a credential.
var secretParamWords = []string{"key", "token", "secret", "password", "passwd", "auth", "sig", "credential"}

// RedactURL returns raw with its password and the values of secret-named
// query parameters (api-key, token, client_secret, sig, ...) replaced by
// REDACTED, so an error can still say where a request went. A URL that does
// not parse is not shown at all.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return unparsableURL
	}
	if _, ok := u.User.Password(); ok {
		u.User = url.UserPassword(u.User.Username(), "REDACTED")
	}
	if u.RawQuery != "" {
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			u.RawQuery = "REDACTED"
		} else {
			for name := range q {
				if isSecretParam(name) {
					for i := range q[name] {
						q[name][i] = "REDACTED"
					}
				}
			}
			u.RawQuery = q.Encode()
		}
	}
	u.Fragment, u.RawFragment = "", ""
	return u.String()
}

func isSecretParam(name string) bool {
	n := strings.ToLower(name)
	for _, w := range secretParamWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// redactErr hides the request URL inside err. Both http.NewRequest (a URL
// that does not parse) and http.Client.Do (a failed send) report the URL in
// a *url.Error, and the URL is OPENAI_BASE_URL with its query and userinfo.
// A parse failure's own detail can quote the bad part, so it is dropped too.
func redactErr(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	ue.URL = RedactURL(ue.URL)
	if ue.Op == "parse" {
		ue.URL = unparsableURL
		ue.Err = errors.New("check OPENAI_BASE_URL")
	}
	return err
}
