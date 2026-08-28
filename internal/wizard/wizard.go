package wizard

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/MiloudiMohamed/agy-account-rotator/internal/google"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/history"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/rotate"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/store"
)

const pasteAttempts = 3

// Add captures one or more accounts via browser sign-in links.
func Add(s *store.Store, label string, eng *rotate.Engine) error {
	reader := bufio.NewReader(os.Stdin)
	client := &http.Client{Timeout: 30 * time.Second}

	for i := 1; ; i++ {
		fmt.Printf("\n=== Adding account #%d ===\n", i)
		acct, err := captureOne(s, client, label)
		if err != nil {
			return err
		}
		if err := eng.Activate(acct); err != nil {
			return fmt.Errorf("activating captured account: %w", err)
		}
		fmt.Printf("✓ Captured and activated: %s\n", acct.Email)

		fmt.Print("Add another? [Y/n] ")
		ans, _ := reader.ReadString('\n')
		ans = strings.ToLower(strings.TrimSpace(ans))
		if ans == "n" || ans == "no" {
			break
		}
	}
	return nil
}

func captureOne(s *store.Store, client *http.Client, label string) (*store.Account, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	verifier, challenge, err := google.NewPKCE()
	if err != nil {
		return nil, err
	}
	state, err := google.RandomState()
	if err != nil {
		return nil, err
	}

	signIn := google.AuthCodeURL(state, challenge)
	fmt.Println("1. Open this link in your browser and approve access:")
	fmt.Println()
	fmt.Println("   " + signIn)
	fmt.Println()
	fmt.Println("2. After approving, you will land on a page that fails to load — that is normal.")
	fmt.Printf("   Copy the URL from your browser's address bar (it starts with %s).\n", google.RedirectHost())

	reader := bufio.NewReader(os.Stdin)
	var tok *google.RefreshResponse
	for attempt := 1; attempt <= pasteAttempts; attempt++ {
		fmt.Print("\n3. Paste the URL (or just the code) here: ")
		line, rerr := reader.ReadString('\n')
		if rerr != nil {
			return nil, fmt.Errorf("reading input: %w", rerr)
		}
		code, perr := google.ParsePasted(line)
		if perr != nil {
			fmt.Printf("  ✗ %s — try again (%d/%d)\n", perr, attempt, pasteAttempts)
			continue
		}
		tok, err = google.Exchange(ctx, client, code, verifier)
		if err != nil {
			fmt.Printf("  ✗ exchange failed: %v\n", err)
			fmt.Println("    (codes are single-use and expire in ~10 minutes; press enter below to get a fresh link if needed)")
			if attempt < pasteAttempts {
				fmt.Print("Retry with a fresh link? [Y/n] ")
				ans, _ := reader.ReadString('\n')
				ans = strings.ToLower(strings.TrimSpace(ans))
				if ans == "n" || ans == "no" {
					return nil, fmt.Errorf("authorization failed: %v", err)
				}
				return captureOne(s, client, label) // new verifier + link
			}
			return nil, fmt.Errorf("authorization failed: %v", err)
		}
		break
	}
	if tok == nil {
		return nil, fmt.Errorf("no authorization captured")
	}

	email, err := google.FetchEmail(ctx, client, tok.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("resolving account email: %w", err)
	}

	acct := &store.Account{
		Email:   email,
		Label:   label,
		AddedAt: time.Now().UTC(),
		File: store.TokenFile{
			Token: store.Token{
				AccessToken:  tok.AccessToken,
				TokenType:    tok.TokenType,
				RefreshToken: tok.RefreshToken,
				Expiry:       google.ExpiryFrom(time.Now(), tok.ExpiresIn).Format(time.RFC3339),
			},
			AuthMethod: "consumer",
		},
	}
	if err := s.SaveAccount(acct); err != nil {
		return nil, err
	}
	_ = history.Append(s, history.Event{
		Type:   history.EventAdd,
		Email:  email,
		Detail: label,
	})
	return acct, nil
}
