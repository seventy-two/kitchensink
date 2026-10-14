package osrs

// tracked is what counts towards a pet: each Wise Old Man metric (a skill,
// boss or activity) that can give one, with how to show it and the pet. A
// metric not here (combat skills, Barrows, the Mimic...) is left out,
// so a boss WOM adds later only shows once it's listed. From the wiki's
// Pet page, October 2026.
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
	"clue_scrolls_master":   {Name: "Master clues", Pet: "Bloodhound", Kind: score},
	"guardians_of_the_rift": {Name: "Guardians of the Rift", Pet: "Abyssal protector", Kind: score},
	"soul_wars_zeal":        {Name: "Soul Wars zeal", Pet: "Lil' Creator", Kind: score},

	// Bosses, by kill count.
	"abyssal_sire":                     {Name: "Abyssal Sire", Pet: "Abyssal orphan"},
	"alchemical_hydra":                 {Name: "Alchemical Hydra", Pet: "Ikkle Hydra"},
	"amoxliatl":                        {Name: "Amoxliatl", Pet: "Moxi"},
	"araxxor":                          {Name: "Araxxor", Pet: "Nid"},
	"artio":                            {Name: "Artio", Pet: "Callisto cub"},
	"brutus":                           {Name: "Brutus", Pet: "Beef"},
	"callisto":                         {Name: "Callisto", Pet: "Callisto cub"},
	"calvarion":                        {Name: "Calvar'ion", Pet: "Vet'ion Jr."},
	"cerberus":                         {Name: "Cerberus", Pet: "Hellpuppy"},
	"chambers_of_xeric":                {Name: "Chambers of Xeric", Pet: "Olmlet"},
	"chambers_of_xeric_challenge_mode": {Name: "Chambers of Xeric (CM)", Pet: "Olmlet"},
	"chaos_elemental":                  {Name: "Chaos Elemental", Pet: "Pet chaos elemental"},
	"chaos_fanatic":                    {Name: "Chaos Fanatic", Pet: "Pet chaos elemental"},
	"commander_zilyana":                {Name: "Commander Zilyana", Pet: "Pet Zilyana"},
	"corporeal_beast":                  {Name: "Corporeal Beast", Pet: "Pet dark core"},
	"dagannoth_prime":                  {Name: "Dagannoth Prime", Pet: "Pet Dagannoth Prime"},
	"dagannoth_rex":                    {Name: "Dagannoth Rex", Pet: "Pet Dagannoth Rex"},
	"dagannoth_supreme":                {Name: "Dagannoth Supreme", Pet: "Pet Dagannoth Supreme"},
	"doom_of_mokhaiotl":                {Name: "Doom of Mokhaiotl", Pet: "Dom"},
	"duke_sucellus":                    {Name: "Duke Sucellus", Pet: "Baron"},
	"general_graardor":                 {Name: "General Graardor", Pet: "Pet General Graardor"},
	"giant_mole":                       {Name: "Giant Mole", Pet: "Baby Mole"},
	"grotesque_guardians":              {Name: "Grotesque Guardians", Pet: "Noon"},
	"kalphite_queen":                   {Name: "Kalphite Queen", Pet: "Kalphite Princess"},
	"king_black_dragon":                {Name: "King Black Dragon", Pet: "Prince Black Dragon"},
	"kraken":                           {Name: "Kraken", Pet: "Pet Kraken"},
	"kreearra":                         {Name: "Kree'arra", Pet: "Pet Kree'arra"},
	"kril_tsutsaroth":                  {Name: "K'ril Tsutsaroth", Pet: "Pet K'ril Tsutsaroth"},
	"mad_angel":                        {Name: "Mad Angel", Pet: "Aggy"},
	"maggot_king":                      {Name: "Maggot King", Pet: "Maggot marquess"},
	"nex":                              {Name: "Nex", Pet: "Nexling"},
	"nightmare":                        {Name: "The Nightmare", Pet: "Little Nightmare"},
	"phantom_muspah":                   {Name: "Phantom Muspah", Pet: "Muphin"},
	"phosanis_nightmare":               {Name: "Phosani's Nightmare", Pet: "Little Nightmare"},
	"sarachnis":                        {Name: "Sarachnis", Pet: "Sraracha"},
	"scorpia":                          {Name: "Scorpia", Pet: "Scorpia's offspring"},
	"scurrius":                         {Name: "Scurrius", Pet: "Scurry"},
	"shellbane_gryphon":                {Name: "Shellbane Gryphon", Pet: "Gull"},
	"skotizo":                          {Name: "Skotizo", Pet: "Skotos"},
	"sol_heredit":                      {Name: "Sol Heredit", Pet: "Smol Heredit"},
	"spindel":                          {Name: "Spindel", Pet: "Venenatis spiderling"},
	"tempoross":                        {Name: "Tempoross", Pet: "Tiny tempor"},
	"the_corrupted_gauntlet":           {Name: "Corrupted Gauntlet", Pet: "Youngllef"},
	"the_gauntlet":                     {Name: "The Gauntlet", Pet: "Youngllef"},
	"the_hueycoatl":                    {Name: "The Hueycoatl", Pet: "Huberte"},
	"the_leviathan":                    {Name: "The Leviathan", Pet: "Lil'viathan"},
	"the_royal_titans":                 {Name: "Royal Titans", Pet: "Bran"},
	"the_whisperer":                    {Name: "The Whisperer", Pet: "Wisp"},
	"theatre_of_blood":                 {Name: "Theatre of Blood", Pet: "Lil' Zik"},
	"theatre_of_blood_hard_mode":       {Name: "Theatre of Blood (HM)", Pet: "Lil' Zik"},
	"thermonuclear_smoke_devil":        {Name: "Thermonuclear smoke devil", Pet: "Pet Smoke Devil"},
	"tombs_of_amascut":                 {Name: "Tombs of Amascut", Pet: "Tumeken's guardian"},
	"tombs_of_amascut_expert":          {Name: "Tombs of Amascut (Expert)", Pet: "Tumeken's guardian"},
	"tzkal_zuk":                        {Name: "TzKal-Zuk", Pet: "Jal-Nib-Rek"},
	"tztok_jad":                        {Name: "TzTok-Jad", Pet: "TzRek-Jad"},
	"vardorvis":                        {Name: "Vardorvis", Pet: "Butch"},
	"venenatis":                        {Name: "Venenatis", Pet: "Venenatis spiderling"},
	"vetion":                           {Name: "Vet'ion", Pet: "Vet'ion Jr."},
	"vorkath":                          {Name: "Vorkath", Pet: "Vorki"},
	"wintertodt":                       {Name: "Wintertodt", Pet: "Phoenix"},
	"yama":                             {Name: "Yama", Pet: "Yami"},
	"zalcano":                          {Name: "Zalcano", Pet: "Smolcano"},
	"zulrah":                           {Name: "Zulrah", Pet: "Pet Snakeling"},
}

// kind is what a metric counts.
type kind int

const (
	kills kind = iota // a boss's kill count (or raids, Tempoross...)
	xp                // a skill's experience
	score             // an activity's score: clues done, games, zeal
)

type metric struct {
	Name, Pet string
	Kind      kind
}
