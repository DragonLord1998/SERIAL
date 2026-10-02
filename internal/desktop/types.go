// Package desktop provides the application services shared by the Wails GUI.
package desktop

type Anime struct {
	ID              string        `json:"id"`
	Title           string        `json:"title"`
	URL             string        `json:"url"`
	ImageURL        string        `json:"imageUrl"`
	Source          string        `json:"source"`
	Language        string        `json:"language"`
	Description     string        `json:"description"`
	Genres          []string      `json:"genres"`
	Score           float64       `json:"score"`
	EpisodeCount    int           `json:"episodeCount"`
	MALID           int           `json:"malId,omitempty"`
	Tags            []AnimeTag    `json:"tags,omitempty"`
	Trailer         *AnimeTrailer `json:"trailer,omitempty"`
	MetadataSource  string        `json:"metadataSource,omitempty"`
	AlternateTitles []string      `json:"alternateTitles,omitempty"`
}

type AnimeTag struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type AnimeTrailer struct {
	YouTubeID string `json:"youtubeId"`
	URL       string `json:"url"`
}

type Episode struct {
	Number string `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

type Source struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Language string `json:"language"`
}

type SourceWarning struct {
	Source  string `json:"source"`
	Message string `json:"message"`
}

type SearchResult struct {
	Animes   []Anime         `json:"animes"`
	Warnings []SourceWarning `json:"warnings"`
}

type AnimeFeedback struct {
	Anime     Anime  `json:"anime"`
	Value     string `json:"value"`
	UpdatedAt string `json:"updatedAt"`
}

type ExploreItem struct {
	Anime  Anime  `json:"anime"`
	Reason string `json:"reason"`
}

type ExploreResult struct {
	Items        []ExploreItem `json:"items"`
	Personalized bool          `json:"personalized"`
	Message      string        `json:"message"`
	Warnings     []string      `json:"warnings"`
}

type Settings struct {
	Quality     string `json:"quality"`
	Mode        string `json:"mode"`
	DownloadDir string `json:"downloadDir"`
	MPVPath     string `json:"mpvPath"`
	Upscaler    string `json:"upscaler"`
}

type HistoryEntry struct {
	Anime     Anime   `json:"anime"`
	Episode   Episode `json:"episode"`
	Position  float64 `json:"position"`
	Duration  float64 `json:"duration"`
	UpdatedAt string  `json:"updatedAt"`
}

type Download struct {
	ID         string  `json:"id"`
	Anime      Anime   `json:"anime"`
	Episode    Episode `json:"episode"`
	Status     string  `json:"status"`
	Progress   float64 `json:"progress"`
	Bytes      int64   `json:"bytes"`
	TotalBytes int64   `json:"totalBytes"`
	Path       string  `json:"path"`
	Error      string  `json:"error"`
}

type PlayerState struct {
	Active       bool     `json:"active"`
	Paused       bool     `json:"paused"`
	Position     float64  `json:"position"`
	Duration     float64  `json:"duration"`
	Volume       float64  `json:"volume"`
	Speed        float64  `json:"speed"`
	Anime        *Anime   `json:"anime,omitempty"`
	Episode      *Episode `json:"episode,omitempty"`
	Error        string   `json:"error"`
	Renderer     string   `json:"renderer"`
	Upscaling    bool     `json:"upscaling"`
	VideoWidth   int      `json:"videoWidth"`
	VideoHeight  int      `json:"videoHeight"`
	OutputWidth  int      `json:"outputWidth"`
	OutputHeight int      `json:"outputHeight"`
}

type State struct {
	Settings                Settings        `json:"settings"`
	Sources                 []Source        `json:"sources"`
	History                 []HistoryEntry  `json:"history"`
	Saved                   []Anime         `json:"saved"`
	Downloads               []Download      `json:"downloads"`
	Player                  PlayerState     `json:"player"`
	MPVAvailable            bool            `json:"mpvAvailable"`
	Feedback                []AnimeFeedback `json:"feedback"`
	FeedbackRevision        uint64          `json:"feedbackRevision"`
	MetalFXAvailable        bool            `json:"metalFXAvailable"`
	MetalFXDevice           string          `json:"metalFXDevice"`
	MetalFXReason           string          `json:"metalFXReason"`
	TrailerPreviewAvailable bool            `json:"trailerPreviewAvailable"`
}
