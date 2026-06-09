package entity_manager

// GenreAllowlist is the canonical set of genre strings used for autocomplete
// suggestions in UIs. It is NOT used for validation — the Open Audio Protocol
// accepts any non-empty genre string up to 100 characters.
var GenreAllowlist = map[string]struct{}{
	"Acoustic": {}, "Alternative": {}, "Ambient": {}, "Audiobooks": {}, "Blues": {},
	"Classical": {}, "Comedy": {}, "Country": {}, "Dancehall": {}, "Deep House": {},
	"Devotional": {}, "Disco": {}, "Downtempo": {}, "Drum & Bass": {}, "Dubstep": {},
	"Electro": {}, "Electronic": {}, "Experimental": {}, "Folk": {}, "Funk": {},
	"Future Bass": {}, "Future House": {}, "Glitch Hop": {}, "Hardstyle": {},
	"Hip-Hop/Rap": {}, "House": {}, "Hyperpop": {}, "Jazz": {}, "Jersey Club": {},
	"Jungle": {}, "Kids": {}, "Latin": {}, "Lo-Fi": {}, "Metal": {}, "Moombahton": {},
	"Podcasts": {}, "Pop": {}, "Progressive House": {}, "Punk": {}, "R&B/Soul": {},
	"Reggae": {}, "Rock": {}, "Soundtrack": {}, "Spoken Word": {}, "Tech House": {},
	"Techno": {}, "Trance": {}, "Trap": {}, "Tropical House": {}, "Vaporwave": {}, "World": {},
}
