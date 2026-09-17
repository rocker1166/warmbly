package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InboxTagResult is one classified inbound message.
type InboxTagResult struct {
	ID               uuid.UUID
	OrganizationID   uuid.UUID
	EmailAccountID   uuid.UUID
	MessageID        string
	ThreadID         string
	Kind             string
	KindConfidence   float64
	KindSource       string
	Intent           string
	IntentConfidence float64
	Relevance        int
	Priority         string
	NeedsReview      bool
	ReviewReason     string
	Answers          json.RawMessage
	Labels           []string
	Model            string
	InputTokens      int
	CreatedAt        time.Time
}

type InboxTagRepository interface {
	Claim(ctx context.Context, orgID, accountID uuid.UUID, messageID, threadID string) (bool, error)
	ReleaseClaim(ctx context.Context, orgID uuid.UUID, messageID string) error
	Save(ctx context.Context, r *InboxTagResult) error
	// ListForReview backs the phase-1 review page: what was decided, how
	// confident it was, and what it would have done.
	ListForReview(ctx context.Context, orgID uuid.UUID, limit, offset int, needsReviewOnly bool) ([]InboxTagResult, int, error)
	ReviewSummary(ctx context.Context, orgID uuid.UUID) (InboxTagReviewSummary, error)

	// ListUntagged and PreviousOutbound back the historical backfill.
	ListUntagged(ctx context.Context, orgID uuid.UUID, since time.Time, limit int) ([]BackfillCandidate, error)
	PreviousOutbound(ctx context.Context, accountID uuid.UUID, threadID string, before time.Time) (string, string, error)

	// ThreadStates backs the follow-up sweep: who spoke last, when, and how far
	// the thread ever got.
	ThreadStates(ctx context.Context, orgID uuid.UUID, since time.Time, limit int) ([]ThreadFollowUpState, error)

	// ClearUnreadable drops the verdicts that came back under the confidence
	// floor, so they are classified again. Used after the criteria change:
	// a stored verdict is only as good as the wording that produced it.
	ClearUnreadable(ctx context.Context, orgID uuid.UUID) ([]string, error)
}

type inboxTagRepository struct {
	db *pgxpool.Pool
}

func NewInboxTagRepository(db *pgxpool.Pool) InboxTagRepository {
	return &inboxTagRepository{db: db}
}

func (r *inboxTagRepository) Claim(ctx context.Context, orgID, accountID uuid.UUID, messageID, threadID string) (bool, error) {
	if messageID == "" {
		return false, nil
	}
	const q = `
		INSERT INTO inbox_tag_results (
			organization_id, email_account_id, message_id, thread_id, status, claimed_at
		) VALUES ($1, $2, $3, $4, 'processing', NOW())
		ON CONFLICT (organization_id, message_id) DO UPDATE
		SET email_account_id = EXCLUDED.email_account_id,
		    thread_id = EXCLUDED.thread_id,
		    claimed_at = NOW(),
		    updated_at = NOW()
		WHERE inbox_tag_results.status = 'processing'
		  AND inbox_tag_results.claimed_at < NOW() - INTERVAL '15 minutes'
		RETURNING id
	`
	var id uuid.UUID
	if err := r.db.QueryRow(ctx, q, orgID, accountID, messageID, threadID).Scan(&id); err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (r *inboxTagRepository) ReleaseClaim(ctx context.Context, orgID uuid.UUID, messageID string) error {
	_, err := r.db.Exec(ctx, `
		DELETE FROM inbox_tag_results
		WHERE organization_id = $1 AND message_id = $2 AND status = 'processing'
	`, orgID, messageID)
	return err
}

func (r *inboxTagRepository) Save(ctx context.Context, res *InboxTagResult) error {
	const q = `
		INSERT INTO inbox_tag_results (
			organization_id, email_account_id, message_id, thread_id,
			kind, kind_confidence, kind_source, intent, intent_confidence,
			relevance, priority, needs_review, review_reason, answers, labels, model, input_tokens
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (organization_id, message_id) DO UPDATE SET
			email_account_id = EXCLUDED.email_account_id,
			thread_id = EXCLUDED.thread_id,
			kind = EXCLUDED.kind,
			kind_confidence = EXCLUDED.kind_confidence,
			kind_source = EXCLUDED.kind_source,
			intent = EXCLUDED.intent,
			intent_confidence = EXCLUDED.intent_confidence,
			relevance = EXCLUDED.relevance,
			priority = EXCLUDED.priority,
			needs_review = EXCLUDED.needs_review,
			review_reason = EXCLUDED.review_reason,
			answers = EXCLUDED.answers,
			labels = EXCLUDED.labels,
			model = EXCLUDED.model,
			input_tokens = EXCLUDED.input_tokens,
			status = 'complete',
			updated_at = NOW()
	`
	answers := res.Answers
	if len(answers) == 0 {
		answers = json.RawMessage(`{}`)
	}
	labels := res.Labels
	if labels == nil {
		labels = []string{}
	}
	_, err := r.db.Exec(ctx, q,
		res.OrganizationID, res.EmailAccountID, res.MessageID, res.ThreadID,
		res.Kind, res.KindConfidence, res.KindSource, res.Intent, res.IntentConfidence,
		res.Relevance, res.Priority, res.NeedsReview, res.ReviewReason, answers, labels, res.Model, res.InputTokens,
	)
	return err
}

func (r *inboxTagRepository) ListForReview(ctx context.Context, orgID uuid.UUID, limit, offset int, needsReviewOnly bool) ([]InboxTagResult, int, error) {
	where := `WHERE organization_id = $1 AND status = 'complete'`
	if needsReviewOnly {
		where += ` AND needs_review`
	}

	var total int
	if err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM inbox_tag_results `+where, orgID).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.db.Query(ctx, `
		SELECT id, organization_id, email_account_id, message_id, thread_id,
		       kind, kind_confidence, kind_source, intent, intent_confidence,
		       relevance, priority, needs_review, review_reason, answers, labels, model, input_tokens, created_at
		FROM inbox_tag_results `+where+`
		ORDER BY relevance DESC, created_at DESC
		LIMIT $2 OFFSET $3`, orgID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]InboxTagResult, 0, limit)
	for rows.Next() {
		var x InboxTagResult
		if err := rows.Scan(
			&x.ID, &x.OrganizationID, &x.EmailAccountID, &x.MessageID, &x.ThreadID,
			&x.Kind, &x.KindConfidence, &x.KindSource, &x.Intent, &x.IntentConfidence,
			&x.Relevance, &x.Priority, &x.NeedsReview, &x.ReviewReason, &x.Answers, &x.Labels, &x.Model, &x.InputTokens, &x.CreatedAt,
		); err != nil {
			return nil, 0, err
		}
		out = append(out, x)
	}
	return out, total, rows.Err()
}

type InboxTagReviewSummary struct {
	Total       int
	NeedsReview int
	FromOffline int
}

func (r *inboxTagRepository) ReviewSummary(ctx context.Context, orgID uuid.UUID) (InboxTagReviewSummary, error) {
	const q = `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE needs_review),
		       COUNT(*) FILTER (WHERE kind_source = 'header')
		FROM inbox_tag_results
		WHERE organization_id = $1 AND status = 'complete'
	`
	var out InboxTagReviewSummary
	err := r.db.QueryRow(ctx, q, orgID).Scan(&out.Total, &out.NeedsReview, &out.FromOffline)
	return out, err
}

// BackfillCandidate is one historical message the backfill may classify.
type BackfillCandidate struct {
	EmailAccountID uuid.UUID
	UserID         uuid.UUID
	MessageID      string
	ThreadID       string
	Subject        string
	BodyText       string
	FromAddr       string
	InternalDate   time.Time
}

// ListUntagged returns inbound messages that have never been classified, newest
// first, for the backfill.
//
// Three exclusions, all deliberate:
//
//   - folder = 'inbox' only. Our own sends are never classified, and the folder
//     is the fact that says which is which. Reading direction from content is
//     how our own outbound gets labelled a human reply at 0.94 confidence.
//   - a sender that is one of our own mailboxes is dropped even inside the
//     inbox folder: mail between two connected mailboxes lands in the second
//     one's inbox and is still ours.
//   - anything already in inbox_tag_results, so a re-run resumes rather than
//     repeats. Same key the live path is idempotent on.
func (r *inboxTagRepository) ListUntagged(ctx context.Context, orgID uuid.UUID, since time.Time, limit int) ([]BackfillCandidate, error) {
	const q = `
		SELECT ue.email_id, ue.user_id, ue.message_id, ue.thread_id,
		       ue.subject, ue.body_text, COALESCE(ue.from_addr[1], ''), ue.internal_date
		FROM unibox_emails ue
		JOIN email_accounts ea ON ea.id = ue.email_id
		WHERE ea.organization_id = $1
		  AND ue.folder = 'inbox'
		  AND ue.internal_date >= $2
		  AND ue.message_id <> ''
		  AND LOWER(COALESCE(
		        NULLIF((regexp_match(COALESCE(ue.from_addr[1], ''), '<([^<>]+)>\s*$'))[1], ''),
		        NULLIF((regexp_match(COALESCE(ue.from_addr[1], ''), '\(([^()]+)\)\s*$'))[1], ''),
		        TRIM(COALESCE(ue.from_addr[1], ''))
		      ))
		      NOT IN (SELECT LOWER(email) FROM email_accounts WHERE organization_id = $1)
		  AND NOT EXISTS (
		        SELECT 1 FROM inbox_tag_results r
			        WHERE r.organization_id = $1 AND r.message_id = ue.message_id
			          AND (r.status = 'complete' OR r.claimed_at >= NOW() - INTERVAL '15 minutes')
		      )
		ORDER BY ue.internal_date DESC
		LIMIT $3
	`
	rows, err := r.db.Query(ctx, q, orgID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BackfillCandidate
	for rows.Next() {
		var c BackfillCandidate
		if err := rows.Scan(&c.EmailAccountID, &c.UserID, &c.MessageID, &c.ThreadID,
			&c.Subject, &c.BodyText, &c.FromAddr, &c.InternalDate); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PreviousOutbound is the plain text of the last message we sent in a thread
// before a given moment.
//
// Without it a reply cannot be read: "yes", "that works" and "sounds good" are
// answers, and the question they answer is not in them. Giving the model our
// side of the exchange is what lets the reply mean anything.
func (r *inboxTagRepository) PreviousOutbound(ctx context.Context, accountID uuid.UUID, threadID string, before time.Time) (string, string, error) {
	if threadID == "" {
		return "", "", nil
	}
	const q = `
		SELECT ue.body_text, COALESCE(c.name, '')
		FROM unibox_emails ue
		LEFT JOIN tasks t
		       ON t.email_account_id = ue.email_id
		      AND t.task_type = 'campaign'
		      AND BTRIM(t.message_id, '<> ') = BTRIM(ue.message_id, '<> ')
		LEFT JOIN campaign_tasks ct ON ct.task_id = t.id
		LEFT JOIN campaigns c ON c.id = ct.campaign_id
		WHERE ue.email_id = $1 AND ue.thread_id = $2 AND ue.folder = 'sent' AND ue.internal_date < $3
		ORDER BY ue.internal_date DESC
		LIMIT 1
	`
	var body, campaign string
	if err := r.db.QueryRow(ctx, q, accountID, threadID, before).Scan(&body, &campaign); err != nil {
		// No previous message is the normal case for the first inbound of a
		// thread, not an error worth failing a classification over.
		return "", "", nil
	}
	return body, campaign, nil
}

// ThreadFollowUpState is one thread's follow-up facts. Every field is read from
// the database; none of it is inferred, and none of it is asked of a model.
type ThreadFollowUpState struct {
	ThreadID       string
	LastInboundAt  time.Time
	LastOutboundAt time.Time
	// BestIntent is the most recent trusted intent in this thread.
	BestIntent string
	// LastKind is the classified kind of the newest inbound message, which is
	// what says whether the "reply" was a person or a mail server.
	LastKind string
}

// ThreadStates returns follow-up facts for every thread with activity since a
// cutoff.
//
// Scoped through email_accounts because unibox_emails carries no organization
// of its own. Follow-up labels use the same organization-plus-thread key as the
// rest of the unibox.
func (r *inboxTagRepository) ThreadStates(ctx context.Context, orgID uuid.UUID, since time.Time, limit int) ([]ThreadFollowUpState, error) {
	const q = `
	WITH scoped_emails AS (
		SELECT ue.*
		FROM unibox_emails ue
		JOIN email_accounts ea ON ea.id = ue.email_id
		WHERE ea.organization_id = $1 AND ue.thread_id <> ''
	),
	active_threads AS (
		SELECT DISTINCT thread_id
		FROM scoped_emails
		WHERE internal_date >= $2
	),
	threads AS (
			SELECT ue.thread_id,
			       MAX(ue.internal_date) FILTER (WHERE ue.folder = 'inbox') AS last_in,
			       MAX(ue.internal_date) FILTER (WHERE ue.folder = 'sent')  AS last_out
			FROM scoped_emails ue
			JOIN active_threads active ON active.thread_id = ue.thread_id
			GROUP BY ue.thread_id
	),
	latest_inbound AS (
		SELECT DISTINCT ON (ue.thread_id)
		       ue.thread_id, ue.email_id, ue.message_id
		FROM scoped_emails ue
		JOIN active_threads active ON active.thread_id = ue.thread_id
		WHERE ue.folder = 'inbox'
		ORDER BY ue.thread_id, ue.internal_date DESC
		)
		SELECT t.thread_id, t.last_in, t.last_out,
		       COALESCE(best.intent, ''), COALESCE(newest.kind, '')
		FROM threads t
		LEFT JOIN LATERAL (
			SELECT r.intent
			FROM inbox_tag_results r
			JOIN scoped_emails ue
			  ON ue.email_id = r.email_account_id
			 AND ue.thread_id = r.thread_id
			 AND ue.message_id = r.message_id
			WHERE r.organization_id = $1 AND r.thread_id = t.thread_id
			  AND r.status = 'complete' AND r.review_reason <> 'intent' AND r.intent <> ''
			ORDER BY ue.internal_date DESC, r.created_at DESC LIMIT 1
		) best ON TRUE
		LEFT JOIN latest_inbound latest ON latest.thread_id = t.thread_id
		LEFT JOIN inbox_tag_results newest
		  ON newest.organization_id = $1
		 AND newest.email_account_id = latest.email_id
		 AND newest.thread_id = latest.thread_id
		 AND newest.message_id = latest.message_id
		 AND newest.status = 'complete'
		 AND newest.review_reason <> 'kind'
		WHERE t.last_out IS NOT NULL
		ORDER BY GREATEST(COALESCE(t.last_in, 'epoch'::timestamptz), t.last_out) DESC
		LIMIT $3
	`
	rows, err := r.db.Query(ctx, q, orgID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ThreadFollowUpState
	for rows.Next() {
		var st ThreadFollowUpState
		var lastIn, lastOut *time.Time
		if err := rows.Scan(&st.ThreadID, &lastIn, &lastOut, &st.BestIntent, &st.LastKind); err != nil {
			return nil, err
		}
		if lastIn != nil {
			st.LastInboundAt = *lastIn
		}
		if lastOut != nil {
			st.LastOutboundAt = *lastOut
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// ClearUnreadable removes the needs-review verdicts for a workspace and returns
// the threads they were on, so their needs-review label can be cleared too.
//
// This is the retune path. Relevance weights can be re-applied to stored
// answers for free, but a change to what the model is ASKED invalidates the
// answer itself, and the only way to fix that is to ask again. Scoped to the
// verdicts that were under the floor, because those are the ones a wording
// change is meant to rescue; a confident answer is left alone.
func (r *inboxTagRepository) ClearUnreadable(ctx context.Context, orgID uuid.UUID) ([]string, error) {
	rows, err := r.db.Query(ctx, `
		DELETE FROM inbox_tag_results
		WHERE organization_id = $1 AND needs_review
		RETURNING thread_id
	`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var threads []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		if t != "" {
			threads = append(threads, t)
		}
	}
	return threads, rows.Err()
}
