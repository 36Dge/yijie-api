package nonprodbootstrap

import (
	"context"
	"errors"
	"testing"
)

func TestVerifyExecutionStateRejectsUnsupportedAuthorityBeforeDatabase(t *testing.T) {
	for _, test := range []struct {
		name    string
		profile string
		state   string
	}{
		{name: "generic profile", profile: "", state: ExecutionStateEmpty},
		{name: "FEAT-125 profile", profile: LocalLabProfile, state: ExecutionStateEmpty},
		{name: "unknown profile", profile: "unknown", state: ExecutionStateEmpty},
		{name: "unknown state", profile: Feat126S10LocalLabProfile, state: "partial"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := VerifyExecutionState(context.Background(), nil, test.profile, test.state)
			if !errors.Is(err, ErrInvalidExecutionState) {
				t.Fatalf("expected invalid execution state, got %v", err)
			}
		})
	}
}
