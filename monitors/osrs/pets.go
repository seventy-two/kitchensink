package osrs

import "strings"

// tracked is what counts towards a pet: each Wise Old Man metric (a skill,
// boss or activity) that can give one, with how to show it and the pet. A
// metric not here (combat skills, Barrows, the Mimic...) is left out,
// so a boss WOM adds later only shows once it's listed. From the wiki's
// Pet page, October 2026, with the pet's drop rate (none for skills, where
// it depends on level and what's being trained). Orion (The Fractured
// Archive, 20 Oct 2026) has no published rate yet.
var tracked = map[string]metric{
	// Skilling pets, by XP.
	"agility":      {Name: "Agility", Pet: "Giant squirrel", Kind: xp},
	"farming":      {Name: "Farming", Pet: "Tangleroot", Kind: xp},
	"fishing":      {Name: "Fishing", Pet: "Heron", Kind: xp},
	"hunter":       {Name: "Hunter", Pet: "Baby chinchompa", Kind: xp},
	"mining":       {Name: "Mining", Pet: "Rock golem", Kind: xp},
	"runecrafting": {Name: "Runecraft", Pet: "Rift guardian", Kind: xp},
	"sailing":      {Name: "Sailing", Pet: "Soup", Kind: xp},
	"thieving":     {Name: "Thieving", Pet: "Rocky", Kind: xp},
	"woodcutting":  {Name: "Woodcutting", Pet: "Beaver", Kind: xp},

	// Activities.
	"clue_scrolls_master":   {Name: "Master clues", Pet: "Bloodhound", Rate: "1/1,000", Kind: score},
	"guardians_of_the_rift": {Name: "Guardians of the Rift", Pet: "Abyssal protector", Rate: "1/4,000", Kind: score},
	"soul_wars_zeal":        {Name: "Soul Wars zeal", Pet: "Lil' Creator", Rate: "1/400 spoils", Kind: score},

	// Bosses, by kill count.
	"abyssal_sire":                     {Name: "Abyssal Sire", Pet: "Abyssal orphan", Rate: "1/2,560"},
	"alchemical_hydra":                 {Name: "Alchemical Hydra", Pet: "Ikkle Hydra", Rate: "1/3,000"},
	"amoxliatl":                        {Name: "Amoxliatl", Pet: "Moxi", Rate: "1/3,000"},
	"araxxor":                          {Name: "Araxxor", Pet: "Nid", Rate: "1/3,000"},
	"artio":                            {Name: "Artio", Pet: "Callisto cub", Rate: "1/2,800"},
	"brutus":                           {Name: "Brutus", Pet: "Beef", Rate: "1/1,000"},
	"callisto":                         {Name: "Callisto", Pet: "Callisto cub", Rate: "1/1,500"},
	"calvarion":                        {Name: "Calvar'ion", Pet: "Vet'ion Jr.", Rate: "1/2,800"},
	"cerberus":                         {Name: "Cerberus", Pet: "Hellpuppy", Rate: "1/3,000"},
	"chambers_of_xeric":                {Name: "Chambers of Xeric", Pet: "Olmlet", Rate: "1/53 purples"},
	"chambers_of_xeric_challenge_mode": {Name: "Chambers of Xeric (CM)", Pet: "Olmlet", Rate: "1/53 purples"},
	"chaos_elemental":                  {Name: "Chaos Elemental", Pet: "Pet chaos elemental", Rate: "1/300"},
	"chaos_fanatic":                    {Name: "Chaos Fanatic", Pet: "Pet chaos elemental", Rate: "1/1,000"},
	"commander_zilyana":                {Name: "Commander Zilyana", Pet: "Pet Zilyana", Rate: "1/5,000"},
	"corporeal_beast":                  {Name: "Corporeal Beast", Pet: "Pet dark core", Rate: "1/5,000"},
	"dagannoth_prime":                  {Name: "Dagannoth Prime", Pet: "Pet Dagannoth Prime", Rate: "1/5,000"},
	"dagannoth_rex":                    {Name: "Dagannoth Rex", Pet: "Pet Dagannoth Rex", Rate: "1/5,000"},
	"dagannoth_supreme":                {Name: "Dagannoth Supreme", Pet: "Pet Dagannoth Supreme", Rate: "1/5,000"},
	"doom_of_mokhaiotl":                {Name: "Doom of Mokhaiotl", Pet: "Dom", Rate: "1/250–1/1,000"},
	"duke_sucellus":                    {Name: "Duke Sucellus", Pet: "Baron", Rate: "1/2,500"},
	"general_graardor":                 {Name: "General Graardor", Pet: "Pet General Graardor", Rate: "1/5,000"},
	"giant_mole":                       {Name: "Giant Mole", Pet: "Baby Mole", Rate: "1/3,000"},
	"grotesque_guardians":              {Name: "Grotesque Guardians", Pet: "Noon", Rate: "1/3,000"},
	"kalphite_queen":                   {Name: "Kalphite Queen", Pet: "Kalphite Princess", Rate: "1/3,000"},
	"king_black_dragon":                {Name: "King Black Dragon", Pet: "Prince Black Dragon", Rate: "1/3,000"},
	"kraken":                           {Name: "Kraken", Pet: "Pet Kraken", Rate: "1/3,000"},
	"kreearra":                         {Name: "Kree'arra", Pet: "Pet Kree'arra", Rate: "1/5,000"},
	"kril_tsutsaroth":                  {Name: "K'ril Tsutsaroth", Pet: "Pet K'ril Tsutsaroth", Rate: "1/5,000"},
	"mad_angel":                        {Name: "Mad Angel", Pet: "Aggy", Rate: "1/2,000"},
	"maggot_king":                      {Name: "Maggot King", Pet: "Maggot marquess", Rate: "1/3,500"},
	"nex":                              {Name: "Nex", Pet: "Nexling", Rate: "1/500"},
	"nightmare":                        {Name: "The Nightmare", Pet: "Little Nightmare", Rate: "1/800–1/4,000"},
	"phantom_muspah":                   {Name: "Phantom Muspah", Pet: "Muphin", Rate: "1/2,500"},
	"phosanis_nightmare":               {Name: "Phosani's Nightmare", Pet: "Little Nightmare", Rate: "1/1,400"},
	"sarachnis":                        {Name: "Sarachnis", Pet: "Sraracha", Rate: "1/3,000"},
	"scorpia":                          {Name: "Scorpia", Pet: "Scorpia's offspring", Rate: "1/2,016"},
	"scurrius":                         {Name: "Scurrius", Pet: "Scurry", Rate: "1/3,000"},
	"shellbane_gryphon":                {Name: "Shellbane Gryphon", Pet: "Gull", Rate: "1/3,000"},
	"skotizo":                          {Name: "Skotizo", Pet: "Skotos", Rate: "1/65"},
	"sol_heredit":                      {Name: "Sol Heredit", Pet: "Smol Heredit", Rate: "1/200"},
	"spindel":                          {Name: "Spindel", Pet: "Venenatis spiderling", Rate: "1/2,800"},
	"tempoross":                        {Name: "Tempoross", Pet: "Tiny tempor", Rate: "1/8,000"},
	"the_corrupted_gauntlet":           {Name: "Corrupted Gauntlet", Pet: "Youngllef", Rate: "1/800"},
	"the_gauntlet":                     {Name: "The Gauntlet", Pet: "Youngllef", Rate: "1/2,000"},
	"the_fractured_archive":            {Name: "The Fractured Archive", Pet: "Orion", Rate: "Unknown"},
	"the_hueycoatl":                    {Name: "The Hueycoatl", Pet: "Huberte", Rate: "1/400"},
	"the_leviathan":                    {Name: "The Leviathan", Pet: "Lil'viathan", Rate: "1/2,500"},
	"the_royal_titans":                 {Name: "Royal Titans", Pet: "Bran", Rate: "1/3,000"},
	"the_whisperer":                    {Name: "The Whisperer", Pet: "Wisp", Rate: "1/2,000"},
	"theatre_of_blood":                 {Name: "Theatre of Blood", Pet: "Lil' Zik", Rate: "1/650"},
	"theatre_of_blood_hard_mode":       {Name: "Theatre of Blood (HM)", Pet: "Lil' Zik", Rate: "1/500"},
	"thermonuclear_smoke_devil":        {Name: "Thermonuclear smoke devil", Pet: "Pet Smoke Devil", Rate: "1/3,000"},
	"tombs_of_amascut":                 {Name: "Tombs of Amascut", Pet: "Tumeken's guardian", Rate: "Varies"},
	"tombs_of_amascut_expert":          {Name: "Tombs of Amascut (Expert)", Pet: "Tumeken's guardian", Rate: "Varies"},
	"tzkal_zuk":                        {Name: "TzKal-Zuk", Pet: "Jal-Nib-Rek", Rate: "1/100"},
	"tztok_jad":                        {Name: "TzTok-Jad", Pet: "TzRek-Jad", Rate: "1/200"},
	"vardorvis":                        {Name: "Vardorvis", Pet: "Butch", Rate: "1/3,000"},
	"venenatis":                        {Name: "Venenatis", Pet: "Venenatis spiderling", Rate: "1/1,500"},
	"vetion":                           {Name: "Vet'ion", Pet: "Vet'ion Jr.", Rate: "1/1,500"},
	"vorkath":                          {Name: "Vorkath", Pet: "Vorki", Rate: "1/3,000"},
	"wintertodt":                       {Name: "Wintertodt", Pet: "Phoenix", Rate: "1/5,000"},
	"yama":                             {Name: "Yama", Pet: "Yami", Rate: "1/2,500"},
	"zalcano":                          {Name: "Zalcano", Pet: "Smolcano", Rate: "1/2,250"},
	"zulrah":                           {Name: "Zulrah", Pet: "Pet Snakeling", Rate: "1/4,000"},
}

// kind is what a metric counts.
type kind int

const (
	kills kind = iota // a boss's kill count (or raids, Tempoross...)
	xp                // a skill's experience
	score             // an activity's score: clues done, games, zeal
)

type metric struct {
	Name, Pet, Rate string
	Kind            kind
}

// petKey is a pet's name for matching, whoever wrote it: the wiki's "Pet
// kraken", the game's "Kraken", any case.
func petKey(name string) string {
	k := strings.ToLower(strings.TrimSpace(name))
	return strings.TrimPrefix(k, "pet ")
}
