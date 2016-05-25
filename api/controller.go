package api

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/spolu/settl/api/lib/authentication"
	"github.com/spolu/settl/facts"
	"github.com/spolu/settl/lib/errors"
	"github.com/spolu/settl/lib/format"
	"github.com/spolu/settl/lib/livemode"
	"github.com/spolu/settl/lib/respond"
	"github.com/spolu/settl/lib/svc"
	"github.com/stellar/go-stellar-base/horizon"

	"golang.org/x/net/context"
)

const (
	// DefaultRetrieveChallengesCount is the default number of challenges
	// returned by the API if the count attribute is not specified.
	DefaultRetrieveChallengesCount = uint64(10)
	// MaxRetrieveChallengesCount is the maximium number of challenges that can
	// be retrieved.
	MaxRetrieveChallengesCount = uint64(10)
)

// clients maps livemodes to Horizon clients.
var clients = map[bool]*horizon.Client{
	false: horizon.DefaultTestNetClient,
	true:  horizon.DefaultPublicNetClient,
}

// usernameRegexp is used to validate usernames at user creation.
var usernameRegexp = regexp.MustCompile(
	"^[a-z0-9]{1,256}$")

// emailRegexp is used to validate emails at user creation.
var emailRegexp = regexp.MustCompile(
	"^[a-z0-9_\\.\\+\\-]+@[a-z0-9-]+\\.[a-z0-9-\\.]+$")

// emailVerifiers is the list of trusted verifiers for emails by livemode.
var emailVerifiers = map[bool][]string{
	true: []string{
		// onboarding
		"GBTIKKWP5FOCMRSTJS46SCTWC6IKCHWDJMJMP6QLFGNYPRTCY63E5T3N",
	},
	false: []string{
		// onboarding
		"GDFZHVU2PNOFR5KXKDBW72ZF45TXTC6LOOLGJK7XD7V2JYQB4KIOEXKN",
	},
}

type controller struct{}

func (c *controller) RetrieveChallenges(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
) {
	count := DefaultRetrieveChallengesCount
	if attr := r.URL.Query().Get("count"); attr != "" {
		err := error(nil)
		count, err = strconv.ParseUint(attr, 10, 64)
		if err != nil || count >= 100 {
			respond.Error(ctx, w, errors.Trace(
				errors.NewUserError(err,
					400,
					"count_invalid",
					fmt.Sprintf("The count attribute you passed is not valid "+
						"(should be a positive integer smaller than 100): %s",
						attr),
				)))
			return
		}
	}

	challenges := []ChallengeResource{}
	for i := uint64(0); i < count; i++ {
		challenge, created, err :=
			authentication.MintChallenge(ctx, authentication.RootLiveKeypair)
		if err != nil {
			respond.Error(ctx, w, errors.Trace(err)) // 500
			return

		}
		challenges = append(challenges, ChallengeResource{
			Value:   *challenge,
			Created: (*created).UnixNano() / (1000 * 1000),
		})
	}

	respond.Success(ctx, w, svc.Resp{
		"challenges": format.JSONPtr(challenges),
	})
}

func (c *controller) CreateUser(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
) {
	params := UserParams{
		Livemode:      livemode.Get(ctx),
		Address:       authentication.Get(ctx).Address,
		Username:      r.PostFormValue("username"),
		EncryptedSeed: r.PostFormValue("encrypted_seed"),
		Email:         strings.ToLower(r.PostFormValue("email")),
		Verifier:      r.PostFormValue("verifier"),
	}

	if !usernameRegexp.MatchString(params.Username) {
		respond.Error(ctx, w, errors.NewUserError(nil,
			400, "username_invalid",
			"The username provided is invalid. Usernames can use "+
				"alphanumeric lowercased characters only.",
		))
		return
	}
	if !emailRegexp.MatchString(params.Email) || len(params.Email) > 256 {
		respond.Error(ctx, w, errors.NewUserError(nil,
			400, "email_invalid",
			"The email provided appears to be invalid. While email "+
				"verification is a bit tricky, we really try to do our best.",
		))
		return
	}
	_, err := base64.StdEncoding.DecodeString(params.EncryptedSeed)
	if err != nil || len(params.EncryptedSeed) > 256 {
		respond.Error(ctx, w, errors.NewUserError(err,
			400, "encrypted_seed_invalid",
			"The encrypted seed appears to be invalid as it could not be "+
				"decoded using base64 or is longer than 256 characters. The "+
				"encrypted seed should be the XOR of the raw seed and an "+
				"scrypt output of the same length using base64 standard "+
				"encoding.",
		))
		return
	}

	// Check that the account exists, it should have been created by the
	// onboarding process.
	_, err = clients[params.Livemode].LoadAccount(params.Address)
	if err != nil {
		respond.Error(ctx, w, errors.NewUserError(err,
			400, "address_invalid",
			fmt.Sprintf(
				"The address %s is not a valid Stellar address. The address "+
					"must be associated with a valid Stellar account on the "+
					"public network in livemode or the test network in "+
					"testmode.",
				params.Address),
		))
		return
	}

	// Check that we trust the verifier specified.
	found := false
	for _, v := range emailVerifiers[params.Livemode] {
		if v == params.Verifier {
			found = true
		}
	}
	if !found {
		respond.Error(ctx, w, errors.NewUserError(nil,
			400, "email_verifier_unknown",
			fmt.Sprintf(
				"The facts verifier %s is not trusted by this API. The "+
					"trusted verifiers are: %s",
				strings.Join(emailVerifiers[params.Livemode], ", ")),
		))
		return
	}

	// Check the email fact.
	if err := facts.CheckFact(
		params.Livemode,
		params.Address,
		facts.FctEmail,
		params.Email,
		params.Verifier,
	); err != nil {
		respond.Error(ctx, w, errors.NewUserError(nil,
			400, "email_verification_failed",
			fmt.Sprintf(
				"The email address %s could not be checked for %s with "+
					"the facts verifier %s.",
				params.Email, params.Address, params.Verifier),
		))
		return
	}

	// - create account

	// - check that account with same username does not exist
	// - check that account with same address does not exist
	// - check that account with same email address does not exist
	// - check that account with same funding_transaction does not exist

}

func (c *controller) ConfirmUser(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
) {
}

func (c *controller) CreateNativeOperation(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
) {
}

func (c *controller) SubmitNativeOperation(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
) {
}
