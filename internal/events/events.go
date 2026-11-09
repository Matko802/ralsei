package events

type Action struct {
	Label string
	Style string
	Text  string
}

type Event struct {
	Title   string
	Text    string
	Actions []Action
}

func All() []Event {
	return []Event{
		{
			Title: "A stray Bit",
			Text:  "A glowing Bit rolls across the floor and stops at your feet.",
			Actions: []Action{
				{Label: "Pick it up", Style: "success", Text: "(You pick up the Bit. It jingles softly.) Ralsei claps, delighted."},
				{Label: "Give it to Ralsei", Style: "primary", Text: "(Ralsei holds the Bit with both hands like it is treasure.) Thank you! I will keep it safe."},
				{Label: "Kick it", Style: "danger", Text: "(The Bit skitters into a corner.) Ralsei winces, then quietly goes to fetch it."},
			},
		},
		{
			Title: "Scarf trouble",
			Text:  "Ralsei's scarf catches on a chair and he spins in place, too polite to ask for help.",
			Actions: []Action{
				{Label: "Untangle him", Style: "success", Text: "(You untangle the scarf.) Thank you. I could have been stuck there all day."},
				{Label: "Pretend not to notice", Style: "secondary", Text: "(Ralsei eventually frees himself and pretends it never happened.)"},
				{Label: "Laugh", Style: "danger", Text: "(Ralsei laughs along, a little embarrassed, and finally pulls the scarf loose.)"},
			},
		},
		{
			Title: "Tea time",
			Text:  "Ralsei appears with two cups of warm tea and offers you one.",
			Actions: []Action{
				{Label: "Accept", Style: "success", Text: "(You drink the tea together. It tastes like honey and calm.)"},
				{Label: "Ask what kind it is", Style: "primary", Text: "(Ralsei brightens.) It is golden flower tea. I made it myself, so I hope it is okay."},
				{Label: "Decline politely", Style: "secondary", Text: "That is okay! (He leaves the cup on the table.) I will leave it here in case you change your mind."},
			},
		},
		{
			Title: "Dark hallway",
			Text:  "The lights flicker. Something shuffles at the end of the hallway.",
			Actions: []Action{
				{Label: "Investigate", Style: "danger", Text: "(You step forward. Ralsei hurries after you and holds your sleeve.) We should stay together!"},
				{Label: "Hide", Style: "secondary", Text: "(You both duck behind a pillar. Ralsei holds his breath until the shuffling fades.)"},
				{Label: "Hold Ralsei's hand", Style: "success", Text: "(You take Ralsei's hand. His ears turn pink.) Oh! Um. Okay. (He does not let go.)"},
			},
		},
		{
			Title: "A familiar song",
			Text:  "Ralsei starts humming a quiet melody, then stops when he notices you listening.",
			Actions: []Action{
				{Label: "Ask him to continue", Style: "success", Text: "(He hesitates, then keeps humming, a little softer than before.)"},
				{Label: "Hum along", Style: "primary", Text: "(Ralsei matches your note, and the hallway feels warmer for a moment.)"},
				{Label: "Applaud", Style: "secondary", Text: "(He hides his face behind his scarf.) It was nothing, really!"},
			},
		},
		{
			Title: "Lost button",
			Text:  "A small button pops off Ralsei's robe and rolls under a table.",
			Actions: []Action{
				{Label: "Retrieve it", Style: "success", Text: "(You hand the button back.) Ralsei beams. You are so kind!"},
				{Label: "Offer to sew it back", Style: "primary", Text: "(He looks at you with wide eyes.) Would you really? I am not very good at sewing."},
				{Label: "Laugh", Style: "secondary", Text: "(Ralsei covers the spot with his scarf and hopes nobody notices.)"},
			},
		},
	}
}
