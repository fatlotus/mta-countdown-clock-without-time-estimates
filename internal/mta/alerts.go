package mta

import (
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
)

var (
	reBreakTags        = regexp.MustCompile(`(?i)<(?:br\s*/?|/p|/div)>`)
	reListItem         = regexp.MustCompile(`(?i)<li\b[^>]*>`)
	reAllTags          = regexp.MustCompile(`<[^>]+>`)
	reAccessIcon       = regexp.MustCompile(`(?i)\[accessib(?:le|ility)\s+icon\]`)
	reBusIcon          = regexp.MustCompile(`(?i)\[(?:shuttle\s+)?bus\s+icon\]`)
	reMultipleNewlines = regexp.MustCompile(`\n{3,}`)

	reStepA       = regexp.MustCompile(`(?i)(?:at|near|to|from|and|,)\s+([A-Z0-9][A-Za-z0-9\s\-\/\.]+?)\s*(\[[A-Za-z0-9]+\](?:\[[A-Za-z0-9]+\])*)`)
	reBracketTags = regexp.MustCompile(`\[([A-Za-z0-9]+)\]`)
	reLeadingPre  = regexp.MustCompile(`(?i)^(?:at|near|to|from|and)\s+`)
)

func cleanRouteID(r string) string {
	r = strings.TrimSpace(r)
	switch r {
	case "SIR":
		return "SI"
	case "S":
		return "GS"
	default:
		return r
	}
}

// inferAlertStopIDs infers affected station stop IDs from alert header text when stop_ids is omitted.
func inferAlertStopIDs(stations []Station, headerText string, alertRoutes []string, direction string) []string {
	if headerText == "" || len(stations) == 0 {
		return nil
	}

	lower := strings.ToLower(headerText)
	if strings.Contains(lower, "with delays") ||
		strings.Contains(lower, "are delayed") ||
		strings.Contains(lower, "runs every") ||
		strings.Contains(lower, "run every") ||
		strings.Contains(lower, "runs approximately every") {
		return nil
	}

	// Step A — Match <Station Name> [<Route>]([Route])* patterns
	var stepAIDs []string
	matches := reStepA.FindAllStringSubmatch(headerText, -1)
	if len(matches) > 0 {
		seen := make(map[string]bool)
		for _, m := range matches {
			if len(m) < 3 {
				continue
			}
			rawName := strings.TrimSpace(m[1])
			rawName = reLeadingPre.ReplaceAllString(rawName, "")
			rawName = strings.TrimSpace(rawName)
			if rawName == "" {
				continue
			}

			routeTagMatches := reBracketTags.FindAllStringSubmatch(m[2], -1)
			if len(routeTagMatches) == 0 {
				continue
			}
			var routeTags []string
			for _, rtm := range routeTagMatches {
				if len(rtm) >= 2 {
					routeTags = append(routeTags, cleanRouteID(rtm[1]))
				}
			}
			if len(routeTags) == 0 {
				continue
			}

			rawNameLower := strings.ToLower(rawName)
			for _, st := range stations {
				servesRoute := false
				for _, r := range routeTags {
					for _, stR := range st.Routes {
						if cleanRouteID(stR) == r {
							servesRoute = true
							break
						}
					}
					if servesRoute {
						break
					}
				}
				if !servesRoute {
					continue
				}

				stNameLower := strings.ToLower(st.Name)
				if stNameLower == rawNameLower || strings.HasPrefix(stNameLower, rawNameLower+"-") {
					id := st.ID
					if direction != "" {
						id += direction
					}
					if !seen[id] {
						seen[id] = true
						stepAIDs = append(stepAIDs, id)
					}
				}
			}
		}

		isReroute := strings.Contains(lower, "from ") && strings.Contains(lower, " to ") &&
			(strings.Contains(lower, "run via") || strings.Contains(lower, "running via") ||
				strings.Contains(lower, "express from") || strings.Contains(lower, "local from"))

		if len(stepAIDs) > 0 && !isReroute {
			return stepAIDs
		}
	}

	// Step B — Match full station names on alertRoutes
	var preferredBoroughs []string
	if strings.Contains(lower, "manhattan") {
		preferredBoroughs = append(preferredBoroughs, "Manhattan")
	}
	if strings.Contains(lower, "brooklyn") {
		preferredBoroughs = append(preferredBoroughs, "Brooklyn")
	}
	if strings.Contains(lower, "queens") {
		preferredBoroughs = append(preferredBoroughs, "Queens")
	}
	if strings.Contains(lower, "bronx") {
		preferredBoroughs = append(preferredBoroughs, "Bronx")
	}
	if strings.Contains(lower, "staten island") {
		preferredBoroughs = append(preferredBoroughs, "Staten Island")
	}

	var matchedStations []Station
	for _, st := range stations {
		if len(alertRoutes) > 0 {
			servesRoute := false
			for _, ar := range alertRoutes {
				cleanAR := cleanRouteID(ar)
				for _, stR := range st.Routes {
					if cleanRouteID(stR) == cleanAR {
						servesRoute = true
						break
					}
				}
				if servesRoute {
					break
				}
			}
			if !servesRoute {
				continue
			}
		}

		escaped := regexp.QuoteMeta(st.Name)
		reStation := regexp.MustCompile(`(?i)(?:^|[^a-zA-Z0-9])` + escaped + `(?:$|[^a-zA-Z0-9\-])`)
		if reStation.MatchString(headerText) {
			matchedStations = append(matchedStations, st)
		}
	}

	if len(matchedStations) == 0 {
		return nil
	}

	if len(preferredBoroughs) > 0 {
		var boroughMatches []Station
		for _, st := range matchedStations {
			for _, pb := range preferredBoroughs {
				if strings.EqualFold(st.Borough, pb) {
					boroughMatches = append(boroughMatches, st)
					break
				}
			}
		}
		if len(boroughMatches) > 0 {
			matchedStations = boroughMatches
		}
	}

	seen := make(map[string]bool)
	var result []string
	for _, st := range matchedStations {
		id := st.ID
		if direction != "" {
			id += direction
		}
		if !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	return result
}

// cleanAlertText strips HTML formatting, converts accessibility and transit icons,
// unescapes HTML entities, and normalizes line breaks in alert text.
func cleanAlertText(raw string) string {
	if raw == "" {
		return ""
	}
	s := reBreakTags.ReplaceAllString(raw, "\n")
	s = reListItem.ReplaceAllString(s, "\n• ")
	s = reAllTags.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reAccessIcon.ReplaceAllString(s, "♿")
	s = reBusIcon.ReplaceAllString(s, "🚌")

	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	s = strings.Join(lines, "\n")
	s = reMultipleNewlines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// extractTranslatedText extracts plain English or first non-empty translation text from a TranslatedString.
// It prefers "en" or "en-us" first and falls back to other translations only if "en"/"en-us" does not exist or is empty.
// Both paths pass through cleanAlertText so HTML tags (e.g. "<p></p>") are stripped.
func extractTranslatedText(ts *gtfs.TranslatedString) string {
	if ts == nil {
		return ""
	}
	var enText string
	var fallback string
	for _, tr := range ts.GetTranslation() {
		cleaned := cleanAlertText(tr.GetText())
		if cleaned == "" {
			continue
		}
		lang := strings.ToLower(tr.GetLanguage())
		if lang == "en" || lang == "en-us" {
			enText = cleaned
			break
		}
		if fallback == "" {
			fallback = cleaned
		}
	}
	if enText != "" {
		return enText
	}
	return fallback
}

// parseFeedToAlerts parses a GTFS-RT Alerts feed into structured ServiceAlert objects.
// If stations catalog is provided, alerts with missing stop_ids will have affected stations inferred from headerText.
func parseFeedToAlerts(feed *gtfs.FeedMessage, nowSec int64, stations ...[]Station) ([]ServiceAlert, int64) {
	if feed == nil {
		return []ServiceAlert{}, nowSec
	}

	var stList []Station
	if len(stations) > 0 {
		stList = stations[0]
	}

	feedTS := int64(0)
	if feed.GetHeader() != nil {
		feedTS = int64(feed.GetHeader().GetTimestamp())
	}
	if feedTS == 0 {
		feedTS = nowSec
	}

	alerts := make([]ServiceAlert, 0, len(feed.GetEntity()))

	for idx, entity := range feed.GetEntity() {
		if entity == nil {
			continue
		}
		alert := entity.GetAlert()
		if alert == nil {
			continue
		}

		headerText := extractTranslatedText(alert.GetHeaderText())
		if headerText == "" {
			continue
		}
		descriptionText := extractTranslatedText(alert.GetDescriptionText())

		// Active periods filtering:
		// If len(alert.GetActivePeriod()) > 0, keep periods where (end == 0 || end >= nowSec) and start <= nowSec + 14*86400.
		// If all periods in a non-empty GetActivePeriod() were filtered out, skip the alert.
		var activePeriods []AlertTimeWindow
		rawPeriods := alert.GetActivePeriod()
		if len(rawPeriods) > 0 {
			for _, ap := range rawPeriods {
				if ap == nil {
					continue
				}
				start := int64(ap.GetStart())
				end := int64(ap.GetEnd())
				if (end == 0 || end >= nowSec) && start <= nowSec+14*86400 {
					activePeriods = append(activePeriods, AlertTimeWindow{
						Start: start,
						End:   end,
					})
				}
			}
			if len(activePeriods) == 0 {
				continue
			}
		}

		// Informed entities: routes and directional stop IDs
		var routes []string
		seenRoutes := make(map[string]bool)
		var stopIDs []string
		seenStopIDs := make(map[string]bool)
		hasDir0 := false
		hasDir1 := false

		for _, ie := range alert.GetInformedEntity() {
			if ie == nil {
				continue
			}
			if r := strings.TrimSpace(ie.GetRouteId()); r != "" {
				if !seenRoutes[r] {
					seenRoutes[r] = true
					routes = append(routes, r)
				}
			}
			if rawStop := strings.TrimSpace(ie.GetStopId()); rawStop != "" {
				baseID := CleanStopID(rawStop)
				var sID string
				if strings.HasSuffix(rawStop, "N") || (ie.DirectionId != nil && ie.GetDirectionId() == 0) {
					sID = baseID + "N"
				} else if strings.HasSuffix(rawStop, "S") || (ie.DirectionId != nil && ie.GetDirectionId() == 1) {
					sID = baseID + "S"
				} else {
					sID = baseID
				}
				if !seenStopIDs[sID] {
					seenStopIDs[sID] = true
					stopIDs = append(stopIDs, sID)
				}
			}
			if ie.DirectionId != nil {
				if ie.GetDirectionId() == 0 {
					hasDir0 = true
				} else if ie.GetDirectionId() == 1 {
					hasDir1 = true
				}
			}
		}

		direction := ""
		if len(stopIDs) == 0 {
			if hasDir0 && !hasDir1 {
				direction = "N"
			} else if hasDir1 && !hasDir0 {
				direction = "S"
			}
			if len(stList) > 0 {
				stopIDs = inferAlertStopIDs(stList, headerText, routes, direction)
			}
		}

		entityID := strings.TrimSpace(entity.GetId())
		if entityID == "" {
			entityID = fmt.Sprintf("alert_%d", idx)
		}

		alertType := "SERVICE ALERT"
		if strings.Contains(strings.ToLower(entityID), "planned_work") {
			alertType = "PLANNED WORK"
		}

		alerts = append(alerts, ServiceAlert{
			ID:            entityID,
			AlertType:     alertType,
			Routes:        routes,
			StopIDs:       stopIDs,
			Direction:     direction,
			HeaderText:    headerText,
			Description:   descriptionText,
			ActivePeriods: activePeriods,
		})
	}

	return alerts, feedTS
}

// parseFeedToAlerts on *Service parses a GTFS-RT Alerts feed using the service station catalog.
func (s *Service) parseFeedToAlerts(feed *gtfs.FeedMessage, nowSec int64) ([]ServiceAlert, int64) {
	return parseFeedToAlerts(feed, nowSec, s.static.Stations)
}

// projectStationAlerts projects system alerts to a target station and its platforms.
func (s *Service) projectStationAlerts(st Station, allowedRoutesSet map[string]bool, alerts []ServiceAlert, nowSec int64) (stationAlerts, northAlerts, southAlerts []ServiceAlert) {
	stationAlerts = []ServiceAlert{}
	northAlerts = []ServiceAlert{}
	southAlerts = []ServiceAlert{}

	for _, al := range alerts {
		// 1. Time window check:
		// An alert is within window if len(al.ActivePeriods) == 0 or any period has
		// (nowSec >= ap.Start && (ap.End == 0 || nowSec <= ap.End)) || (ap.Start > nowSec && ap.Start <= nowSec+86400)
		if len(al.ActivePeriods) > 0 {
			active := false
			for _, ap := range al.ActivePeriods {
				if (nowSec >= ap.Start && (ap.End == 0 || nowSec <= ap.End)) || (ap.Start > nowSec && ap.Start <= nowSec+86400) {
					active = true
					break
				}
			}
			if !active {
				continue
			}
		}

		// 2. Route match check:
		// Route matches if len(al.Routes) == 0 or any r in al.Routes is in allowedRoutesSet (or s.normalizeRouteID(r) is in allowedRoutesSet).
		if len(allowedRoutesSet) > 0 && len(al.Routes) > 0 {
			routeMatched := false
			for _, r := range al.Routes {
				if allowedRoutesSet[r] || allowedRoutesSet[s.normalizeRouteID(r)] {
					routeMatched = true
					break
				}
			}
			if !routeMatched {
				continue
			}
		}

		// 3. Station & direction matching
		stopIDs := al.StopIDs
		if len(stopIDs) == 0 && len(s.static.Stations) > 0 {
			stopIDs = inferAlertStopIDs(s.static.Stations, al.HeaderText, al.Routes, al.Direction)
		}

		if len(stopIDs) == 0 {
			// Line-wide alert
			matchesN := al.Direction == "" || al.Direction == "N"
			matchesS := al.Direction == "" || al.Direction == "S"
			if !matchesN && !matchesS {
				continue
			}
			stationAlerts = append(stationAlerts, al)
			if matchesN {
				northAlerts = append(northAlerts, al)
			}
			if matchesS {
				southAlerts = append(southAlerts, al)
			}
		} else {
			// Station-specific alert
			hasBoth := containsStr(stopIDs, st.ID)
			hasN := hasBoth || containsStr(stopIDs, st.ID+"N")
			hasS := hasBoth || containsStr(stopIDs, st.ID+"S")
			if !hasN && !hasS {
				continue
			}

			stAl := al
			if hasN && !hasS {
				stAl.Direction = "N"
			} else if hasS && !hasN {
				stAl.Direction = "S"
			} else {
				stAl.Direction = ""
			}
			stationAlerts = append(stationAlerts, stAl)

			if hasN {
				northAlerts = append(northAlerts, al)
			}
			if hasS {
				southAlerts = append(southAlerts, al)
			}
		}
	}

	return stationAlerts, northAlerts, southAlerts
}
