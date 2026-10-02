package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFeedbackPersistsSwitchesAndClearsWithoutChangingLibrary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	service, err := NewService(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	anime := Anime{ID: "hianime-test", Title: "Fullmetal Alchemist: Brotherhood", URL: "https://hianime.at/fullmetal-alchemist-brotherhood-1", Source: "hianime", Genres: []string{"Adventure"}}
	if _, err := service.SaveAnime(anime, true); err != nil {
		t.Fatal(err)
	}
	liked, err := service.SetFeedback(anime, "like")
	if err != nil || len(liked.Feedback) != 1 || liked.Feedback[0].Value != "like" {
		t.Fatalf("like failed: %+v %v", liked.Feedback, err)
	}
	if liked.Feedback[0].UpdatedAt == "" || liked.FeedbackRevision != 1 {
		t.Fatal("feedback timestamp missing")
	}
	liked.Feedback[0].Anime.Genres[0] = "Mutation"
	if service.Snapshot().Feedback[0].Anime.Genres[0] != "Adventure" {
		t.Fatal("snapshot exposes feedback state to mutation")
	}
	service.Close()
	reopened, err := NewService(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(reopened.Snapshot().Feedback) != 1 || reopened.Snapshot().Feedback[0].Value != "like" || reopened.Snapshot().FeedbackRevision != 1 {
		t.Fatal("feedback did not survive restart")
	}
	copy := anime
	copy.ID = "other-source-copy"
	copy.Title = "Fullmetal Alchemist Brotherhood"
	disliked, err := reopened.SetFeedback(copy, "dislike")
	if err != nil || len(disliked.Feedback) != 1 || disliked.Feedback[0].Value != "dislike" || disliked.FeedbackRevision != 2 {
		t.Fatalf("switch duplicated rating: %+v %v", disliked.Feedback, err)
	}
	cleared, err := reopened.SetFeedback(anime, "")
	if err != nil || len(cleared.Feedback) != 0 || len(cleared.Saved) != 1 || cleared.FeedbackRevision != 3 {
		t.Fatalf("clear affected library: %+v %v", cleared, err)
	}
}

func TestLegacyStateLoadsWithEmptyFeedback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"saved":[],"history":[],"downloads":[],"settings":{"quality":"720p"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	state := service.Snapshot()
	if state.Feedback == nil || len(state.Feedback) != 0 || state.Settings.Quality != "720p" {
		t.Fatalf("legacy migration changed preferences: %+v", state)
	}
}

func TestFeedbackRejectsInvalidAndRollsBackFailedSave(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	service, err := NewService(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	anime := Anime{ID: "hianime-test", Title: "Naruto", URL: "https://hianime.at/naruto-1335", Source: "hianime"}
	if _, err := service.SetFeedback(anime, "love"); err == nil {
		t.Fatal("invalid rating accepted")
	}
	invalid := anime
	invalid.URL = "https://localhost/naruto-1335"
	if _, err := service.SetFeedback(invalid, "like"); err == nil {
		t.Fatal("invalid catalog URL accepted")
	}
	if _, err := service.SetFeedback(anime, "like"); err != nil {
		t.Fatal(err)
	}
	// A directory cannot be atomically replaced with the saved state file.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetFeedback(anime, "dislike"); err == nil {
		t.Fatal("failed persistence reported success")
	}
	if got := service.Snapshot().Feedback[0].Value; got != "like" {
		t.Fatalf("failed save changed feedback: %s", got)
	}
	if service.Snapshot().FeedbackRevision != 1 {
		t.Fatal("failed save advanced the feedback revision")
	}
}

func TestServiceExploreUsesFeedbackAndPreservesKnownTitlesAfterRestart(t *testing.T) {
	r, fixture := newRecommendationFixture()
	seed := recommendationTestAnime("Favorite", "1")
	related := recommendationTestAnime("Related", "2")
	unrelated := recommendationTestAnime("Unrelated", "3")
	saved := recommendationTestAnime("Saved", "4")
	watched := recommendationTestAnime("Watched", "5")
	fixture.pages["/most-popular"] = []Anime{seed, unrelated, related, saved, watched}
	fixture.pages["/top-airing"] = []Anime{unrelated}
	fixture.details[seed.URL] = hiAnimeDetail{Anime: seed, Related: []Anime{related}}
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	initial.History = []HistoryEntry{{Anime: watched}}
	if err := store.Save(initial); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.recommender = r
	if _, err := service.SaveAnime(saved, true); err != nil {
		t.Fatal(err)
	}
	cold, err := service.GetExplore(false)
	if err != nil || cold.Personalized || len(cold.Items) != 3 {
		t.Fatalf("cold service feed: %+v, %v", cold, err)
	}
	if _, err := service.SetFeedback(seed, "like"); err != nil {
		t.Fatal(err)
	}
	service.Close()
	reopened, err := NewService(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.recommender = r
	personal, err := reopened.GetExplore(false)
	if err != nil || !personal.Personalized || len(personal.Items) != 2 || personal.Items[0].Anime.ID != related.ID || personal.Items[0].Reason != "Because you liked Favorite" {
		t.Fatalf("restored service personalization: %+v, %v", personal, err)
	}
	if _, err := reopened.SetFeedback(related, "dislike"); err != nil {
		t.Fatal(err)
	}
	without, err := reopened.GetExplore(false)
	if err != nil || len(without.Items) != 1 || without.Items[0].Anime.ID != unrelated.ID {
		t.Fatalf("disliked title returned: %+v, %v", without, err)
	}
	for _, anime := range []Anime{seed, related} {
		if _, err := reopened.SetFeedback(anime, ""); err != nil {
			t.Fatal(err)
		}
	}
	restored, err := reopened.GetExplore(false)
	if err != nil || restored.Personalized || len(restored.Items) != 3 || restored.Items[0].Anime.ID != cold.Items[0].Anime.ID {
		t.Fatalf("undo did not restore discovery: %+v, %v", restored, err)
	}
}
