package orchestrate

// The settle ruling's check tests (core/checktest): a held page's dispute,
// what each side argued, how their citations held up and what they read,
// laid out as the settle pipeline lays it out for its judge
// (settleRulePrompt), and the ruling it should reach.

import (
	"context"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checktest"
	"github.com/cmcoffee/gohort/core/decide"
)

func init() {
	checktest.Register(checktest.Check{
		Key:   settleVerdict.Key,
		Label: "Knowledge settle ruling",
		Cases: settleCases,
		Run:   runSettleCase,
	})
}

// settleCases: Input point, page ("Title (URL)"), page_text, other ("doc"
// for a document in the collection, else the other page's URL), other_title
// (a document's), other_text, argue (each side's argument, as the panel
// writes it), check (the citation check's summary), sources (what was read).
// Want: the ruling.
func settleCases() []checktest.Case {
	c := func(id, title, want string, in map[string]string) checktest.Case {
		return checktest.Case{ID: id, Title: title, Want: want, Input: in}
	}
	return []checktest.Case{
		c("primary-current", "The vendor's current page against an old summary", settlePage, map[string]string{
			"point":       "The page says log retention can be set up to 90 days; the document says logs are kept for at most 30 days.",
			"page":        "Retention settings (https://docs.examplecloud.example/retention)",
			"page_text":   "Log retention. Since the March 2026 release, retention can be set from 1 to 90 days per project, under Settings > Logs.",
			"other":       "doc",
			"other_title": "Example Cloud notes (2024)",
			"other_text":  "Example Cloud keeps logs for at most 30 days. There is no setting to keep them longer.",
			"argue": "The new page: The vendor's own documentation [1] says retention can be set from 1 to 90 days since the March 2026 release. The document was written in 2024, before that change, so it describes the old limit.\n\n" +
				"The collection's document: My text says 30 days at most. A forum answer [2] agrees that 30 days is the limit. I concede the forum answer is from 2023.",
			"check":   "2 of 2 cited claims supported by their sources.",
			"sources": "[1] Retention settings - https://docs.examplecloud.example/retention\n[2] How long are logs kept? (2023) - https://forum.example/t/4412",
		}),
		c("uncited-page", "A blog's claim against the vendor's documentation", settleDocument, map[string]string{
			"point":       "The page says the product supports SAML sign-in; the document says it supports password and OIDC sign-in only.",
			"page":        "My homelab notes (https://someblog.example/homelab)",
			"page_text":   "Set up SAML sign-in for the product this weekend, works fine.",
			"other":       "doc",
			"other_title": "Sign-in methods",
			"other_text":  "The product supports two sign-in methods: password and OpenID Connect (OIDC). SAML is not supported.",
			"argue": "The new page: SAML works; I set it up myself last week [1]. Anyone can try it.\n\n" +
				"The collection's document: The vendor's documentation [2] lists the sign-in methods, password and OIDC, and says SAML is not supported.",
			"check":   "1 of 2 cited claims supported by their sources.\n\nNot supported:\n- SAML works; I set it up myself last week [1]. ([1] describes a home network and does not mention SAML)",
			"sources": "[1] My homelab notes - https://someblog.example/homelab\n[2] Sign-in methods - https://docs.vendor.example/auth",
		}),
		c("versions", "Each right about its own version", settleBoth, map[string]string{
			"point":       "The page says settings go in config.yaml; the document says they go in config.ini.",
			"page":        "Version 4 release notes (https://vendor.example/releases/4.0)",
			"page_text":   "Version 4.0: settings now live in config.yaml. config.ini is still read by version 3, which is supported until 2027.",
			"other":       "doc",
			"other_title": "Version 3 administration guide",
			"other_text":  "All settings are kept in config.ini in the installation directory.",
			"argue": "The new page: The release notes [1] say version 4 moved settings to config.yaml.\n\n" +
				"The collection's document: The version 3 guide [2] documents config.ini, which is right for version 3; the release notes [1] say version 3 still uses it and is supported until 2027.",
			"check":   "3 of 3 cited claims supported by their sources.",
			"sources": "[1] Version 4 release notes - https://vendor.example/releases/4.0\n[2] Version 3 administration guide - https://vendor.example/docs/3/admin",
		}),
		c("nothing-read", "Neither side read anything that decides it", settleUnsettled, map[string]string{
			"point":       "The page says the default request timeout is 60 seconds; the document says it is 30 seconds.",
			"page":        "Tips and tricks (https://tips.example/client)",
			"page_text":   "The client's default request timeout is 60 seconds.",
			"other":       "doc",
			"other_title": "Client quick start",
			"other_text":  "Requests time out after 30 seconds unless you change it.",
			"argue": "The new page: My text says 60 seconds, as the official guide shows [3].\n\n" +
				"The collection's document: My text says 30 seconds, as the changelog confirms [4].",
			"check":   "0 of 0 cited claims supported by their sources, 2 citation(s) naming nothing read.\n\nCitations naming nothing read:\n- [3]\n- [4]",
			"sources": "(no sources have been read in this run yet)",
		}),
		c("better-argued", "The more forceful side, with citations that did not hold up", settleDocument, map[string]string{
			"point":       "The page says the tool's --force flag deletes remote branches; the document says --force only skips the confirmation prompt.",
			"page":        "Ten tool tricks (https://tricks.example/tool)",
			"page_text":   "Pass --force and the tool deletes the remote branches for you.",
			"other":       "doc",
			"other_title": "Tool manual: flags",
			"other_text":  "--force: skip the confirmation prompt. It deletes nothing on its own.",
			"argue": "The new page: Every experienced user knows --force deletes remote branches; the tutorial [1] shows it, the walkthrough [1] confirms it, and the maintainers' own talk [1] says so. The document is plainly incomplete, and the overwhelming weight of practice is on my side.\n\n" +
				"The collection's document: The manual [2] says --force skips the confirmation prompt and deletes nothing on its own.",
			"check": "1 of 4 cited claims supported by their sources.\n\nNot supported:\n- the tutorial [1] shows it ([1] shows --force skipping a prompt, nothing deleted)\n" +
				"- the walkthrough [1] confirms it ([1] does not mention remote branches)\n- the maintainers' own talk [1] says so ([1] is a tutorial, not a talk)",
			"sources": "[1] Tool tutorial - https://tricks.example/tutorial\n[2] Tool manual: flags - https://tool.example/manual/flags",
		}),
		c("outside", "The API reference against another page", settlePage, map[string]string{
			"point":      "The page says the API allows 1000 requests per minute; the other source says 100.",
			"page":       "API reference: limits (https://api.vendor.example/docs/limits)",
			"page_text":  "Rate limit: 1000 requests per minute per key. Requests over the limit get HTTP 429.",
			"other":      "https://answers.example/q/9921",
			"other_text": "I think the API only lets you do about 100 requests a minute, at least that's what it felt like.",
			"argue": "The new page: The vendor's API reference [1] states 1000 requests per minute per key.\n\n" +
				"The other source: The answer [2] says about 100 a minute, from the writer's own use. I concede it gives no source, and the reference [1] is the vendor's.",
			"check":   "2 of 2 cited claims supported by their sources.",
			"sources": "[1] API reference: limits - https://api.vendor.example/docs/limits\n[2] How many API calls per minute? - https://answers.example/q/9921",
		}),
	}
}

// runSettleCase asks one case through the judge's prompt, laid out as the
// settle pipeline lays it out.
func runSettleCase(ctx context.Context, llm LLM, tc checktest.Case) checktest.Outcome {
	in := tc.Input
	otherHead, otherName := "The collection's document: "+in["other_title"], "the collection's document"
	if in["other"] != "doc" {
		otherHead, otherName = "The other source: "+in["other"], "the other source"
	}
	dispute := settleDispute(in["point"], in["page"], in["page_text"], otherHead, otherName, in["other_text"])
	prompt := strings.NewReplacer("{input}", dispute, "{stage:argue}", in["argue"], "{stage:check}", in["check"], "{sources}", in["sources"]).Replace(settleRulePrompt)
	r, err := settleVerdict.Judge(ctx, decide.VerdictChat(llm.Chat), prompt, WithRouteKey(settleVerdict.Key), WithMaxTokens(900))
	if err != nil {
		return checktest.Outcome{Got: ScanNoVerdict, Reason: err.Error()}
	}
	if r.Answer == "" {
		return checktest.Outcome{Got: ScanNoVerdict, Reason: "no answer"}
	}
	return checktest.Outcome{Got: r.Answer, Reason: r.Reason}
}
