package policy

// Matrix ist die Default-Autonomiematrix (Spec 14.3), indiziert nach Level 0..3.
var Matrix = map[Class][4]Verdict{
	Read:          {Ask, Allow, Allow, Allow},
	WriteInternal: {Ask, Allow, Allow, Allow},
	Compute:       {Ask, Allow, Allow, Allow},
	WriteExternal: {Ask, Ask, Review, Review},
	Communicate:   {Ask, Ask, Ask, Review},
	Destructive:   {Ask, Ask, Ask, Ask},
	Spend:         {Ask, Ask, Ask, Ask}, // L3: Regel bis Limit, sonst ask; immer Step-up
	Credential:    {HumanOnly, HumanOnly, HumanOnly, HumanOnly},
	Laptop:        {Ask, Ask, Ask, Ask}, // L3: Regel, sonst ask
}

// riskOf liefert eine grobe Risikoeinstufung für UI/Approval-Karten.
func riskOf(c Class, tainted bool) string {
	switch c {
	case Read, WriteInternal, Compute:
		if tainted {
			return "medium"
		}
		return "low"
	case WriteExternal, Communicate:
		if tainted {
			return "high"
		}
		return "medium"
	default:
		return "high"
	}
}
