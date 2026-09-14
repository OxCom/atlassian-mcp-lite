//go:build selfupdate

package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// repoPath is the project this binary updates itself from, compiled in. There
// is no setting for it: a configurable update source is a configurable place to
// fetch code from, and the signature check only proves that *some* holder of
// the release key signed the artifact — not that the operator meant to take it
// from there.
const repoPath = "OxCom/atlassian-mcp-lite"

// apiBaseURL and downloadBaseURL are vars only so the tests can point them at
// an httptest server. Neither is reachable from outside the package and neither
// is read from the environment.
var (
	apiBaseURL      = "https://api.github.com"
	downloadBaseURL = "https://github.com/" + repoPath + "/releases/download"
)

// githubAccept asks for the documented media type rather than whatever the
// default happens to be on the day.
const githubAccept = "application/vnd.github+json"

// tagRe is the only shape a release tag may have. Everything downstream — the
// URLs, the backup file name, the two version strings in the result — is built
// from a string that matched this, which is what keeps free-form text out of a
// URL path, out of a filename and out of the model's context.
var tagRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// maxTagLen bounds the tag before the regex sees it. The regex itself is
// unbounded: `\d+` is satisfied by a megabyte of digits, which would then be
// interpolated into a URL path and into a filename on disk. Real tags are a
// dozen characters.
const maxTagLen = 32

// errBadTag reports a release tag this build refuses to act on.
var errBadTag = errors.New("release tag is not a plain version")

// latestRelease is the only part of GitHub's release JSON this package will
// look at.
//
// browser_download_url is deliberately absent. It is the field a normal client
// would use, and it is a destination chosen by whoever can edit a release: a
// maintainer account with a stolen session, or a compromised release workflow,
// can point it anywhere without touching a single byte of the signed artifacts.
// Constructing the URLs locally from the tag means the only thing an edited
// release can change is *which version* is offered, and a wrong version still
// has to carry a valid signature. Decoding into this struct rather than into a
// map is what makes "we ignore the rest" a property of the code rather than a
// comment.
type latestRelease struct {
	TagName string `json:"tag_name"`
}

// normalizeTag accepts the running build's version stamp in either spelling and
// returns it in tag form, or an error if it is not a plain version.
//
// core.Version defaults to "0.1.0" and a release build stamps it with
// -X ...Version=v1.2.3, so both spellings genuinely occur. Normalising here
// means the comparison against the latest tag, the backup file name and the
// result payload all speak one dialect.
func normalizeTag(version string) (string, error) {
	v := strings.TrimSpace(version)
	if v != "" && !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return validateTag(v)
}

// validateTag is the single gate every tag passes before it is used for
// anything at all.
func validateTag(tag string) (string, error) {
	if len(tag) > maxTagLen {
		// The value is not quoted: it is unvalidated third-party text of
		// attacker-chosen length, and the result it would appear in is read by
		// a model.
		return "", fmt.Errorf("%w: it is %d bytes, limit is %d", errBadTag, len(tag), maxTagLen)
	}
	if !tagRe.MatchString(tag) {
		// Quoting is safe here only because the length is already bounded, and
		// the operator needs to see what was rejected to tell a malformed
		// release apart from a hijacked response.
		return "", fmt.Errorf("%w: %q; expected the form v1.2.3", errBadTag, tag)
	}
	return tag, nil
}

// latestTag reads the newest published release tag, and nothing else.
func latestTag(ctx context.Context, c *http.Client) (string, error) {
	url := apiBaseURL + "/repos/" + repoPath + "/releases/latest"
	body, err := fetch(ctx, c, "release lookup", url, githubAccept, maxReleaseJSONBytes)
	if err != nil {
		return "", err
	}
	var rel latestRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		// The decoder's message can quote the document it failed on, so it is
		// not wrapped: the useful fact is that the response was not the JSON
		// object this code expects.
		return "", errors.New("release lookup: response is not the expected JSON object")
	}
	return validateTag(rel.TagName)
}

// downloadURL builds a release-asset URL locally. tag must already have passed
// validateTag and name must be a name this package constructed; neither is ever
// a string taken from a response.
func downloadURL(tag, name string) string {
	return downloadBaseURL + "/" + tag + "/" + name
}

// downloadSums fetches the checksum manifest and its detached signature. They
// are fetched together because neither is useful without the other, and because
// a failure to get the signature must never fall through to using the manifest
// unverified.
func downloadSums(ctx context.Context, c *http.Client, tag string) (sums, sig []byte, err error) {
	sums, err = fetch(ctx, c, "checksum manifest", downloadURL(tag, "SHA256SUMS"), "", maxSumsBytes)
	if err != nil {
		return nil, nil, err
	}
	sig, err = fetch(ctx, c, "checksum signature", downloadURL(tag, "SHA256SUMS.sig"), "", maxSigBytes)
	if err != nil {
		return nil, nil, err
	}
	return sums, sig, nil
}

// downloadAsset fetches the binary for this build's platform and variant.
func downloadAsset(ctx context.Context, c *http.Client, tag, name string) ([]byte, error) {
	return fetch(ctx, c, "release asset", downloadURL(tag, name), "", maxAssetBytes)
}
