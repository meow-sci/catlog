package stats

import (
	"database/sql"
	"fmt"
	"slices"
	"testing"

	"github.com/meow-sci/catlog/server/internal/store"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

func withChallengeBatch(t *testing.T, p *store.Projections, flushRows int, fn func(*Batch) error) {
	t.Helper()
	if err := p.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
		b := NewBatch(tx, BatchOptions{FlushRows: flushRows})
		if err := fn(b); err != nil {
			return err
		}
		return b.Flush(t.Context())
	}); err != nil {
		t.Fatal(err)
	}
}

func TestChallengeMembersReadPendingDurableAndEveryScope(t *testing.T) {
	p := testutil.MemProjections(t)
	withChallengeBatch(t, p, 0, func(b *Batch) error {
		for _, tc := range []struct {
			career, system, challenge, member string
		}{
			{"", "", "global", "one"},
			{"save-a", "system-a", "career", "two"},
			{"", "system-a", "system", "three"},
		} {
			added, err := b.AddChallengeMember(t.Context(), 7, tc.career, tc.system, tc.challenge, tc.member, 10)
			if err != nil || !added {
				return fmt.Errorf("first add %v = %v, %v", tc, added, err)
			}
			added, err = b.AddChallengeMember(t.Context(), 7, tc.career, tc.system, tc.challenge, tc.member, 11)
			if err != nil || added {
				return fmt.Errorf("pending duplicate %v = %v, %v", tc, added, err)
			}
			count, err := b.ChallengeMemberCount(t.Context(), 7, tc.career, tc.system, tc.challenge)
			if err != nil || count != 1 {
				return fmt.Errorf("pending count %v = %d, %v", tc, count, err)
			}
		}
		if err := b.Flush(t.Context()); err != nil {
			return err
		}
		added, err := b.AddChallengeMember(t.Context(), 7, "", "system-a", "system", "four", 12)
		if err != nil || !added {
			return fmt.Errorf("post-flush member = %v, %v", added, err)
		}
		count, err := b.ChallengeMemberCount(t.Context(), 7, "", "system-a", "system")
		if err != nil || count != 2 {
			return fmt.Errorf("post-flush cached count = %d, %v", count, err)
		}
		return nil
	})

	withChallengeBatch(t, p, 0, func(b *Batch) error {
		count, err := b.ChallengeMemberCount(t.Context(), 7, "", "system-a", "system")
		if err != nil || count != 2 {
			return fmt.Errorf("durable count = %d, %v", count, err)
		}
		added, err := b.AddChallengeMember(t.Context(), 7, "", "system-a", "system", "three", 1)
		if err != nil || added {
			return fmt.Errorf("durable duplicate = %v, %v", added, err)
		}
		added, err = b.AddChallengeMember(t.Context(), 7, "", "system-a", "system", "five", 13)
		if err != nil || !added {
			return fmt.Errorf("second durable member = %v, %v", added, err)
		}
		count, err = b.ChallengeMemberCount(t.Context(), 7, "", "system-a", "system")
		if err != nil || count != 3 {
			return fmt.Errorf("merged durable+pending count = %d, %v", count, err)
		}
		return nil
	})

	var rows int
	if err := p.Reader().QueryRowContext(t.Context(), `SELECT count(*) FROM challenge_member`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 5 {
		t.Errorf("challenge members = %d, want 5", rows)
	}
	var firstSeq int64
	if err := p.Reader().QueryRowContext(t.Context(), `
		SELECT first_seq FROM challenge_member
		WHERE player_id = 7 AND career = '' AND system = 'system-a'
		  AND challenge = 'system' AND member = 'three'`).Scan(&firstSeq); err != nil {
		t.Fatal(err)
	}
	if firstSeq != 10 {
		t.Errorf("durable duplicate changed first_seq to %d, want 10", firstSeq)
	}
}

func TestChallengeMemberFlushIsChunkedAndDeterministic(t *testing.T) {
	want := []challengeMemberKey{
		{challengeSetKey{1, "", "", "a"}, "m"},
		{challengeSetKey{1, "", "s", "z"}, "a"},
		{challengeSetKey{1, "c", "s", "a"}, "z"},
		{challengeSetKey{2, "", "", "a"}, "m"},
	}
	for _, flushRows := range []int{1, 2, 0, 10_000} {
		t.Run(fmt.Sprintf("flush_rows=%d", flushRows), func(t *testing.T) {
			p := testutil.MemProjections(t)
			var got []challengeMemberKey
			withChallengeBatch(t, p, flushRows, func(b *Batch) error {
				for _, k := range []challengeMemberKey{want[3], want[2], want[1], want[0]} {
					if _, err := b.AddChallengeMember(t.Context(), k.playerID, k.career, k.system, k.challenge, k.member, k.playerID); err != nil {
						return err
					}
				}
				if err := b.Flush(t.Context()); err != nil {
					return err
				}
				got = slices.Clone(b.challengeMemberKeys)
				return nil
			})
			if !slices.Equal(got, want) {
				t.Errorf("flush order = %v, want %v", got, want)
			}
		})
	}
}
