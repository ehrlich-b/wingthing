package relay

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func makeEmbedding(kind string) *SocialEmbedding {
	id := uuid.NewString()
	return &SocialEmbedding{
		ID:           id,
		UserID:       "user-1",
		Text:         "test embedding " + id,
		Embedding:    []byte{1, 2, 3, 4},
		Embedding512: []byte{5, 6, 7, 8},
		Kind:         kind,
		Visible:      true,
		Mass:         1,
		DecayedMass:  1.0,
	}
}

func TestCreateAndGetSocialEmbedding(t *testing.T) {
	s := testStore(t)

	e := makeEmbedding("post")
	link := "https://example.com/post1"
	slug := "my-first-post"
	e.Link = &link
	e.Slug = &slug

	if err := s.CreateSocialEmbedding(e); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetSocialEmbedding(e.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected embedding, got nil")
	}
	if got.ID != e.ID {
		t.Errorf("id = %q, want %q", got.ID, e.ID)
	}
	if got.UserID != "user-1" {
		t.Errorf("user_id = %q, want user-1", got.UserID)
	}
	if got.Link == nil || *got.Link != link {
		t.Errorf("link = %v, want %q", got.Link, link)
	}
	if got.Slug == nil || *got.Slug != slug {
		t.Errorf("slug = %v, want %q", got.Slug, slug)
	}
	if got.Kind != "post" {
		t.Errorf("kind = %q, want post", got.Kind)
	}
	if !got.Visible {
		t.Error("expected visible=true")
	}
}

func TestGetSocialEmbeddingNotFound(t *testing.T) {
	s := testStore(t)

	got, err := s.GetSocialEmbedding("nonexistent")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestGetSocialEmbeddingByLink(t *testing.T) {
	s := testStore(t)

	e := makeEmbedding("post")
	link := "https://example.com/unique-link"
	e.Link = &link
	s.CreateSocialEmbedding(e)

	got, err := s.GetSocialEmbeddingByLink(link)
	if err != nil {
		t.Fatalf("get by link: %v", err)
	}
	if got == nil {
		t.Fatal("expected embedding, got nil")
	}
	if got.ID != e.ID {
		t.Errorf("id = %q, want %q", got.ID, e.ID)
	}
}

func TestGetSocialEmbeddingBySlug(t *testing.T) {
	s := testStore(t)

	e := makeEmbedding("post")
	slug := "unique-slug"
	e.Slug = &slug
	s.CreateSocialEmbedding(e)

	got, err := s.GetSocialEmbeddingBySlug(slug)
	if err != nil {
		t.Fatalf("get by slug: %v", err)
	}
	if got == nil {
		t.Fatal("expected embedding, got nil")
	}
	if got.ID != e.ID {
		t.Errorf("id = %q, want %q", got.ID, e.ID)
	}
}

func TestListAnchors(t *testing.T) {
	s := testStore(t)

	a1 := makeEmbedding("anchor")
	a2 := makeEmbedding("anchor")
	post := makeEmbedding("post")
	hidden := makeEmbedding("anchor")
	hidden.Visible = false

	s.CreateSocialEmbedding(a1)
	s.CreateSocialEmbedding(a2)
	s.CreateSocialEmbedding(post)
	s.CreateSocialEmbedding(hidden)

	anchors, err := s.ListAnchors()
	if err != nil {
		t.Fatalf("list anchors: %v", err)
	}
	if len(anchors) != 2 {
		t.Errorf("count = %d, want 2", len(anchors))
	}
	for _, a := range anchors {
		if a.Kind != "anchor" {
			t.Errorf("kind = %q, want anchor", a.Kind)
		}
	}
}

func TestAssignPostAnchorsAndListByAnchor(t *testing.T) {
	s := testStore(t)

	anchor := makeEmbedding("anchor")
	s.CreateSocialEmbedding(anchor)

	p1 := makeEmbedding("post")
	p1.Mass = 10
	p1.DecayedMass = 10.0
	p1.Upvotes24h = 5
	s.CreateSocialEmbedding(p1)

	p2 := makeEmbedding("post")
	p2.Mass = 1
	p2.DecayedMass = 1.0
	p2.Upvotes24h = 20
	s.CreateSocialEmbedding(p2)

	assignments := []PostAnchor{
		{AnchorID: anchor.ID, Similarity: 0.95},
		{AnchorID: anchor.ID, Similarity: 0.80},
	}
	if err := s.AssignPostAnchors(p1.ID, []PostAnchor{assignments[0]}); err != nil {
		t.Fatalf("assign p1: %v", err)
	}
	if err := s.AssignPostAnchors(p2.ID, []PostAnchor{assignments[1]}); err != nil {
		t.Fatalf("assign p2: %v", err)
	}

	// Test "new" sort
	posts, err := s.ListPostsByAnchor(anchor.ID, "new", 10)
	if err != nil {
		t.Fatalf("list new: %v", err)
	}
	if len(posts) != 2 {
		t.Errorf("new count = %d, want 2", len(posts))
	}

	// Test "hot" sort
	posts, err = s.ListPostsByAnchor(anchor.ID, "hot", 10)
	if err != nil {
		t.Fatalf("list hot: %v", err)
	}
	if len(posts) != 2 {
		t.Errorf("hot count = %d, want 2", len(posts))
	}

	// Test "rising" sort
	posts, err = s.ListPostsByAnchor(anchor.ID, "rising", 10)
	if err != nil {
		t.Fatalf("list rising: %v", err)
	}
	if len(posts) != 2 {
		t.Errorf("rising count = %d, want 2", len(posts))
	}

	// Test "best" sort
	posts, err = s.ListPostsByAnchor(anchor.ID, "best", 10)
	if err != nil {
		t.Fatalf("list best: %v", err)
	}
	if len(posts) != 2 {
		t.Errorf("best count = %d, want 2", len(posts))
	}
	// best = similarity * decayed_mass: p1 = 0.95*10 = 9.5, p2 = 0.80*1 = 0.8
	if len(posts) == 2 && posts[0].ID != p1.ID {
		t.Errorf("best sort: first post should be p1 (higher similarity*mass)")
	}

	// Test limit
	posts, err = s.ListPostsByAnchor(anchor.ID, "new", 1)
	if err != nil {
		t.Fatalf("list limit: %v", err)
	}
	if len(posts) != 1 {
		t.Errorf("limit count = %d, want 1", len(posts))
	}
}

func TestUpvoteIdempotent(t *testing.T) {
	s := testStore(t)

	post := makeEmbedding("post")
	s.CreateSocialEmbedding(post)

	if err := s.Upvote("voter-1", post.ID); err != nil {
		t.Fatalf("upvote 1: %v", err)
	}
	if err := s.Upvote("voter-1", post.ID); err != nil {
		t.Fatalf("upvote 2 (idempotent): %v", err)
	}
	if err := s.Upvote("voter-2", post.ID); err != nil {
		t.Fatalf("upvote voter-2: %v", err)
	}

	count, err := s.GetUpvoteCount(post.ID)
	if err != nil {
		t.Fatalf("get count: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

func TestCreateAndListComments(t *testing.T) {
	s := testStore(t)

	post := makeEmbedding("post")
	s.CreateSocialEmbedding(post)

	c1 := &SocialComment{
		ID:      uuid.NewString(),
		PostID:  post.ID,
		UserID:  "user-1",
		Content: "top-level comment",
	}
	if err := s.CreateComment(c1); err != nil {
		t.Fatalf("create c1: %v", err)
	}

	// Threaded reply
	parentID := c1.ID
	c2 := &SocialComment{
		ID:       uuid.NewString(),
		PostID:   post.ID,
		UserID:   "user-2",
		ParentID: &parentID,
		Content:  "reply to c1",
		IsBot:    true,
	}
	if err := s.CreateComment(c2); err != nil {
		t.Fatalf("create c2: %v", err)
	}

	comments, err := s.ListCommentsByPost(post.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(comments) != 2 {
		t.Errorf("count = %d, want 2", len(comments))
	}

	// Verify thread structure
	var reply *SocialComment
	for _, c := range comments {
		if c.ID == c2.ID {
			reply = c
		}
	}
	if reply == nil {
		t.Fatal("reply comment not found")
	}
	if reply.ParentID == nil || *reply.ParentID != c1.ID {
		t.Errorf("reply parent_id = %v, want %q", reply.ParentID, c1.ID)
	}
	if !reply.IsBot {
		t.Error("expected reply is_bot=true")
	}
}

func TestUpsertAndGetSocialUser(t *testing.T) {
	s := testStore(t)

	u := &SocialUser{
		ID:          uuid.NewString(),
		Provider:    "github",
		ProviderID:  "12345",
		DisplayName: "TestUser",
	}
	if err := s.UpsertSocialUser(u); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.GetSocialUser(u.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected user, got nil")
	}
	if got.DisplayName != "TestUser" {
		t.Errorf("display_name = %q, want TestUser", got.DisplayName)
	}
	if got.IsPro {
		t.Error("expected is_pro=false")
	}

	// Upsert with updated name
	u.DisplayName = "UpdatedUser"
	u.IsPro = true
	if err := s.UpsertSocialUser(u); err != nil {
		t.Fatalf("upsert update: %v", err)
	}

	got, err = s.GetSocialUser(u.ID)
	if err != nil {
		t.Fatalf("get updated: %v", err)
	}
	if got.DisplayName != "UpdatedUser" {
		t.Errorf("display_name = %q, want UpdatedUser", got.DisplayName)
	}
	if !got.IsPro {
		t.Error("expected is_pro=true after upsert")
	}
}

func TestGetSocialUserNotFound(t *testing.T) {
	s := testStore(t)

	got, err := s.GetSocialUser("nonexistent")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestGetSocialUserByProvider(t *testing.T) {
	s := testStore(t)

	u := &SocialUser{
		ID:          uuid.NewString(),
		Provider:    "google",
		ProviderID:  "g-999",
		DisplayName: "GoogleUser",
	}
	s.UpsertSocialUser(u)

	got, err := s.GetSocialUserByProvider("google", "g-999")
	if err != nil {
		t.Fatalf("get by provider: %v", err)
	}
	if got == nil {
		t.Fatal("expected user, got nil")
	}
	if got.ID != u.ID {
		t.Errorf("id = %q, want %q", got.ID, u.ID)
	}

	// Not found
	got, err = s.GetSocialUserByProvider("google", "nonexistent")
	if err != nil {
		t.Fatalf("get by provider not found: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestRateLimitFreeUser(t *testing.T) {
	s := testStore(t)

	// Free user: bucket size 2, starts full
	ok, err := s.CheckRateLimit("free-user", "post", false)
	if err != nil {
		t.Fatalf("check 1: %v", err)
	}
	if !ok {
		t.Error("expected allowed on first call")
	}

	ok, err = s.CheckRateLimit("free-user", "post", false)
	if err != nil {
		t.Fatalf("check 2: %v", err)
	}
	if !ok {
		t.Error("expected allowed on second call")
	}

	// Third call should be rate limited (bucket size 2, consumed 2)
	ok, err = s.CheckRateLimit("free-user", "post", false)
	if err != nil {
		t.Fatalf("check 3: %v", err)
	}
	if ok {
		t.Error("expected rate limited on third call")
	}
}

func TestRateLimitProUser(t *testing.T) {
	s := testStore(t)

	// Pro user: bucket size 5
	for i := 0; i < 5; i++ {
		ok, err := s.CheckRateLimit("pro-user", "post", true)
		if err != nil {
			t.Fatalf("check %d: %v", i+1, err)
		}
		if !ok {
			t.Errorf("expected allowed on call %d", i+1)
		}
	}

	// 6th call should be rate limited
	ok, err := s.CheckRateLimit("pro-user", "post", true)
	if err != nil {
		t.Fatalf("check 6: %v", err)
	}
	if ok {
		t.Error("expected rate limited on 6th call")
	}
}

func TestRateLimitRefill(t *testing.T) {
	s := testStore(t)

	// Exhaust free bucket
	s.CheckRateLimit("refill-user", "post", false)
	s.CheckRateLimit("refill-user", "post", false)

	// Manually set last_refill to 1 day ago so tokens refill
	yesterday := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02 15:04:05")
	s.db.Exec("UPDATE social_rate_limits SET tokens = 0, last_refill = ? WHERE user_id = 'refill-user'", yesterday)

	// After 1 day at 5/day rate, we should have ~5 tokens (capped at bucket size 2)
	ok, err := s.CheckRateLimit("refill-user", "post", false)
	if err != nil {
		t.Fatalf("check after refill: %v", err)
	}
	if !ok {
		t.Error("expected allowed after refill")
	}
}

func TestDecayMasses(t *testing.T) {
	s := testStore(t)

	post := makeEmbedding("post")
	post.Mass = 100
	post.DecayedMass = 100.0
	s.CreateSocialEmbedding(post)

	if err := s.DecayMasses(); err != nil {
		t.Fatalf("decay: %v", err)
	}

	got, err := s.GetSocialEmbedding(post.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Just created, so age is ~0 days, decayed_mass should be close to mass
	if got.DecayedMass < 99.0 || got.DecayedMass > 100.0 {
		t.Errorf("decayed_mass = %f, expected ~100", got.DecayedMass)
	}
}

func TestRefreshUpvotes24h(t *testing.T) {
	s := testStore(t)

	post := makeEmbedding("post")
	s.CreateSocialEmbedding(post)

	s.Upvote("v1", post.ID)
	s.Upvote("v2", post.ID)

	if err := s.RefreshUpvotes24h(); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	got, err := s.GetSocialEmbedding(post.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Upvotes24h != 2 {
		t.Errorf("upvotes_24h = %d, want 2", got.Upvotes24h)
	}
}

func TestUpdateAnchorEffectiveAndCentroid(t *testing.T) {
	s := testStore(t)

	anchor := makeEmbedding("anchor")
	s.CreateSocialEmbedding(anchor)

	eff := []byte{10, 20, 30}
	if err := s.UpdateAnchorEffective(anchor.ID, eff); err != nil {
		t.Fatalf("update effective: %v", err)
	}

	cen := []byte{40, 50, 60}
	if err := s.UpdateAnchorCentroid(anchor.ID, cen); err != nil {
		t.Fatalf("update centroid: %v", err)
	}

	got, err := s.GetSocialEmbedding(anchor.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got.Effective512) != string(eff) {
		t.Errorf("effective_512 = %v, want %v", got.Effective512, eff)
	}
	if string(got.Centroid512) != string(cen) {
		t.Errorf("centroid_512 = %v, want %v", got.Centroid512, cen)
	}
}

func TestLinkUniqueConstraint(t *testing.T) {
	s := testStore(t)

	link := "https://example.com/dupe"
	e1 := makeEmbedding("post")
	e1.Link = &link
	if err := s.CreateSocialEmbedding(e1); err != nil {
		t.Fatalf("create e1: %v", err)
	}

	e2 := makeEmbedding("post")
	e2.Link = &link
	err := s.CreateSocialEmbedding(e2)
	if err == nil {
		t.Error("expected error on duplicate link")
	}
}

func TestSlugUniqueConstraint(t *testing.T) {
	s := testStore(t)

	slug := "dupe-slug"
	e1 := makeEmbedding("post")
	e1.Slug = &slug
	if err := s.CreateSocialEmbedding(e1); err != nil {
		t.Fatalf("create e1: %v", err)
	}

	e2 := makeEmbedding("post")
	e2.Slug = &slug
	err := s.CreateSocialEmbedding(e2)
	if err == nil {
		t.Error("expected error on duplicate slug")
	}
}

func TestNullLinkAllowsMultiple(t *testing.T) {
	s := testStore(t)

	e1 := makeEmbedding("post")
	e2 := makeEmbedding("post")
	// Both have nil link — should not conflict
	if err := s.CreateSocialEmbedding(e1); err != nil {
		t.Fatalf("create e1: %v", err)
	}
	if err := s.CreateSocialEmbedding(e2); err != nil {
		t.Fatalf("create e2: %v", err)
	}
}
