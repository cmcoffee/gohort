package admin

import (
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// systemSections is the system part of the admin page: System Status, Site Settings, Channel Wake Rules, Add account, Users, Feature Access, Default Apps, App Groups.
func (a *AdminApp) systemSections() []ui.Section {
	return []ui.Section{
		{
			Title:    "System Status",
			Subtitle: "Live readiness summary. Refreshes every 10 seconds.",
			Body: ui.DisplayPanel{
				Source:        "api/status",
				AutoRefreshMS: 10000,
				Pairs: []ui.DisplayPair{
					{Label: "TLS enabled", Field: "tls_enabled"},
					{Label: "TLS self-signed", Field: "tls_self_signed"},
					{Label: "Auth enabled", Field: "auth_enabled"},
					{Label: "User count", Field: "user_count"},
					{Label: "Active sessions", Field: "active_sessions"},
					{Label: "Public signup", Field: "allow_signup"},
					// What is confining LLM-issued shell commands on this
					// host. Sits in System Status rather than behind a
					// tools page because when it reads "none" it applies
					// to every tool at once.
					{Label: "Shell sandbox", Field: "sandbox_backend", StatusField: "sandbox_status"},
					{Label: "Shell tools confined", Field: "sandbox_confined", StatusField: "sandbox_status"},
					{Label: "Sandbox required (fail closed)", Field: "sandbox_required"},
					// The row an operator on an unconfined host actually
					// needs: not "are we confined" (no) but "is anything
					// running" (also no). Reading only the row above, the
					// natural conclusion is the dangerous one.
					{Label: "Shell tools refused (no sandbox)", Field: "sandbox_refusing", StatusField: "sandbox_status"},
					{Label: "Unsandboxed bypass (off / admin / on)", Field: "sandbox_bypass"},
					// Reach and consumption are different questions, and
					// the rows above answer only the first.
					{Label: "Resource limits per command", Field: "sandbox_limits"},
					{Label: "Sandbox note", Field: "sandbox_advice"},
				},
			},
		},
		{
			Title:    "Site Settings",
			Subtitle: "Authentication, naming, and operational quotas. Saved automatically as you edit.",
			Body: ui.FormPanel{
				Source: "api/settings",
				Fields: []ui.FormField{
					{Field: "allow_signup", Label: "Allow public signup", Type: "toggle",
						Help: "When off, only existing accounts can sign in. Approvals can still happen via the user list."},
					{Field: "ui_theme", Label: "Theme", Type: "select",
						Options: themePickerOptions(),
						Help:    "Platform-wide UI theme. Reload the page after saving to see it."},
					{Field: "service_name", Label: "Service name", Type: "text",
						Placeholder: "gohort", Help: "Shown in the page title and email From: line."},
					{Field: "doc_brand", Label: "Document brand", Type: "text",
						Placeholder: "e.g. SnugLab Research",
						Help:        "Header label on exported documents (guide PDF/HTML) and the PDF branding line. Falls back to the site name."},
					{Field: "site_name", Label: "Site name", Type: "text",
						Placeholder: "e.g. SnugLab", Help: "Shown in exported-document footers."},
					{Field: "external_url", Label: "External URL", Type: "text",
						Placeholder: "https://gohort.example.com",
						Help:        "Used to build links in notification emails. Include scheme."},
					{Field: "timezone", Label: "Timezone", Type: "select",
						Options: TimezoneSelectOptions("System default (host zone)"),
						Help:    "Deployment timezone for day boundaries (cost/usage), schedules, and displayed times. Blank uses the host zone. Applies on restart."},
					{Field: "notify_from", Label: "Notification From", Type: "text",
						Placeholder: "noreply@example.com"},
					{Field: "session_days", Label: "Session idle lifetime (days)", Type: "number",
						Min: 1, Max: 90, Help: "Default 7. How long a session survives WITHOUT use — it renews while someone is actively working, so this is the idle timeout, not a countdown from login."},
					{Field: "session_absolute_days", Label: "Session maximum age (days)", Type: "number",
						Min: 0, Max: 3650, Help: "Default 90. The ceiling renewal cannot cross, counted from login — what still forces a fresh sign-in eventually and bounds a stolen session cookie. 0 removes the ceiling, so a session in continuous use never expires."},
					{Field: "max_login_attempts", Label: "Max login attempts", Type: "number",
						Min: 1, Max: 100, Help: "Default 5. Failed attempts above this trigger a temporary lockout."},
					{Field: "lockout_minutes", Label: "Lockout duration (minutes)", Type: "number",
						Min: 1, Max: 1440, Help: "Default 15."},
					{Field: "fetch_cache_quota_mb", Label: "Fetch cache quota (MB)", Type: "number",
						Min: 0, Max: 10240, Help: "Disk budget for the URL fetch cache. 0 disables caching."},
				},
			},
		},
		{
			Title:    "Channel Wake Rules",
			Subtitle: "Master gatekeeper applied to every channel before an inbound message wakes its agent. One rule per line; rules are OR'd (a message that matches ANY rule wakes the agent). These merge on top of each channel's own per-channel rules (set in the channel rail). Leave blank to apply no global rule.",
			Body: ui.FormPanel{
				Source: "api/settings",
				Fields: []ui.FormField{
					{Field: "channel_wake_rules", Label: "Master rules", Type: "textarea", Rows: 5,
						Placeholder: "Respond only when called by name\nAlways respond to a direct 1:1 message",
						Help:        "A cheap worker-LLM check runs these on each inbound. Follow-ups to the agent's own last message bypass the rules; owner messages are evaluated like anyone else's unless a rule says otherwise."},
				},
			},
		},
		{
			Title:    "Add account",
			Subtitle: "Invite a new user by email (they click a link and set their own password), or set a password directly. To reset an existing user's password, use the Reset password button on their row below.",
			Body:     ui.Card{HTML: userAdminHTML},
		},
		{
			Title:    "Users",
			Subtitle: "Approve pending signups, grant or revoke admin, manage app access, sign someone out of every browser, or delete accounts. Deleting revokes every credential the account holds — sessions, access tokens, desktop and bridge keys, connected accounts. Pending users see a placeholder page until approved.",
			Body: ui.Table{
				Source: "api/users",
				RowKey: "username",
				Columns: []ui.Col{
					{Field: "username", Flex: 1},
				},
				RowActions: []ui.RowAction{
					// Admin toggle — partial PUT with {admin: bool}.
					// Label clarifies what the switch controls
					// (without it, the lone switch in the row was
					// ambiguous — users couldn't tell if it was
					// "user enabled" or "admin role").
					{
						Type:   "toggle",
						Field:  "admin",
						Label:  "Admin",
						PostTo: "api/users/{username}",
						Method: "PUT",
					},
					// Reset password — per-row modal (set a new one, or send a
					// reset link). "client" so the modal can show the returned
					// link for manual copy when mail isn't configured.
					{Type: "button", Label: "Reset password", Method: "client",
						PostTo: "admin_reset_password", Compact: true, HideIf: "pending"},
					// Approve / reject — visible only while the user is pending.
					{Type: "button", Label: "Approve", PostTo: "api/users/{username}/approve",
						Method: "POST", OnlyIf: "pending"},
					{Type: "button", Label: "Reject", PostTo: "api/users/{username}/reject",
						Method: "POST", OnlyIf: "pending", Variant: "danger"},
					// Apps access via chip picker. RecordSource hits the
					// per-user GET we just added so the picker shows the
					// user's actual current apps (not the global list).
					//
					// "App access" and not "Apps": the bare noun had come to
					// mean two things, this one (which apps a PERSON may
					// open) and the operator controls ON an app, which live
					// on their own tab. Inside a user's row the qualifier
					// costs nothing and the noun was the ambiguous half.
					func() ui.RowAction {
						a := ui.Expand("App access", ui.ChipPicker{
							OptionsSource: "api/apps",
							RecordSource:  "api/users/{username}",
							Field:         "apps",
							PostTo:        "api/users/{username}/apps",
							Method:        "PUT",
							NameField:     "path", // value stored in user.apps[]
							LabelField:    "name", // friendly label rendered on the chip
						})
						a.Compact = true
						return a
					}(),
					// App groups — assign whole bundles of apps at once.
					// Value stored is the group ID; access resolves the group
					// to its apps at check time. Sits alongside App access: a
					// user's access is the union of both.
					//
					// "App groups" for the same reason its neighbour is "App
					// access": a bare "Groups" in a user's row reads as user
					// groups, which are not a thing here, and the section
					// that manages these is already called App Groups.
					func() ui.RowAction {
						a := ui.Expand("App groups", ui.ChipPicker{
							OptionsSource: "api/app-groups",
							RecordSource:  "api/users/{username}",
							Field:         "groups",
							PostTo:        "api/users/{username}/groups",
							Method:        "PUT",
							NameField:     "id",   // value stored in user.groups[]
							LabelField:    "name", // friendly label rendered on the chip
						})
						a.Compact = true
						return a
					}(),
					// Sign out everywhere — ends every live session without
					// touching the account. The answer to a lost laptop, which
					// previously had none: a session only ended when the person
					// holding it clicked Log out, and sliding renewal means one
					// in use keeps pushing its own expiry out.
					{Type: "button", Label: "Sign out everywhere", Method: "POST",
						PostTo:  "api/users/{username}/revoke-sessions",
						Confirm: "Sign this user out of every browser? They keep their account and can log back in.",
						Compact: true, HideIf: "pending"},
					// Delete with confirm — danger variant.
					{Type: "button", Label: "Delete", PostTo: "api/users/{username}",
						Method:  "DELETE",
						Confirm: "Delete this user permanently? Their sessions, access tokens, desktop and bridge keys, and connected accounts are all revoked with them.",
						Variant: "danger"},
				},
				EmptyText: "No users yet.",
			},
		},
		{
			Title:    "Feature Access",
			Subtitle: "Which users may expose outward-facing surfaces through their own personal access tokens. Reaching the OpenAI /v1 endpoint bypasses cookie auth (it's guarded only by a token), so this is the gate on who may use it at all. Empty = every user (the surface's own per-key scope still applies); listing users restricts it to them. Each feature is declared by its app.",
			Body: ui.Table{
				Source: "api/feature-access",
				RowKey: "feature",
				Columns: []ui.Col{
					{Field: "label", Flex: 1},
					{Field: "access", Label: "Allowed", Flex: 0},
					{Field: "desc", Flex: 2, Mute: true},
				},
				RowActions: []ui.RowAction{
					ui.Expand("Set users", ui.ACLPicker(ui.ACLPickerConfig{
						OptionsSource: "api/user-candidates",
						RecordSource:  "api/feature-access?feature={feature}",
						Field:         "allowed_users",
						PostTo:        "api/feature-access?feature={feature}",
						Method:        "POST",
						Noun:          "user",
						Intro:         "Which users may use this feature. Empty = every user. Each granted user then decides which of THEIR keys use it and what each key may reach.",
						EmptyText:     "No users to grant yet.",
						// "Allowed" on the row is this list, summarised. A
						// picker broadcasts nothing on its own, so setting
						// users left the column saying what it said before.
						Invalidate: []string{"api/feature-access"},
					})),
				},
				EmptyText: "No shareable features are registered.",
			},
		},
		{
			Title:    "Default Apps",
			Subtitle: "Apps every newly-approved user gets access to by default. Per-user overrides above take precedence.",
			Body: ui.ChipPicker{
				OptionsSource: "api/apps",
				RecordSource:  "api/settings",
				Field:         "default_apps",
				PostTo:        "api/settings",
				Method:        "PUT",
				NameField:     "path",
				LabelField:    "name",
			},
		},
		{
			Title:    "App Groups",
			Subtitle: "Bundle apps under one name (e.g. \"Writers\", \"Ops\"), then assign a whole group to a user from the Groups picker above — access resolves the group to its apps, so editing a group instantly re-provisions everyone assigned to it.",
			Body: ui.Stack{
				Children: []ui.Component{
					// Create a new group (name + optional description). The
					// per-row editor below fills in its apps.
					ui.FormPanel{
						PostURL:     "api/app-groups",
						Method:      "POST",
						SubmitLabel: "Create group",
						Fields: []ui.FormField{
							{Field: "name", Type: "text", Label: "Name", Placeholder: "e.g. Writers"},
							{Field: "description", Type: "text", Label: "Description", Placeholder: "Optional note"},
						},
						Invalidate: []string{"api/app-groups"},
					},
					// Existing groups: per-row editor (name/description + the
					// apps chip picker) and delete.
					ui.Table{
						Source: "api/app-groups",
						RowKey: "id",
						Columns: []ui.Col{
							{Field: "name", Flex: 1},
							{Field: "description", Flex: 2, Mute: true},
						},
						RowActions: []ui.RowAction{
							ui.Expand("Edit", ui.Stack{
								Children: []ui.Component{
									ui.FormPanel{
										Source:  "api/app-groups/{id}",
										PostURL: "api/app-groups",
										Method:  "POST",
										Fields: []ui.FormField{
											{Field: "name", Type: "text", Label: "Name"},
											{Field: "description", Type: "text", Label: "Description"},
										},
									},
									// Which apps this group grants.
									ui.ChipPicker{
										OptionsSource: "api/apps",
										RecordSource:  "api/app-groups/{id}",
										Field:         "apps",
										PostTo:        "api/app-groups",
										Method:        "POST",
										NameField:     "path",
										LabelField:    "name",
									},
								},
							}),
							{Type: "button", Label: "Delete",
								PostTo:  "api/app-groups?id={id}",
								Method:  "DELETE",
								Confirm: "Delete this app group? Users assigned to it lose the apps it granted (unless they also have them via another grant).",
								Variant: "danger"},
						},
						EmptyText: "No app groups yet. Create one above, then add its apps and assign it to users.",
					},
				},
			},
		},
	}
}

// themePickerOptions builds the Theme dropdown from the core/ui theme
// registry, so a newly-registered theme appears here with no edit.
func themePickerOptions() []ui.SelectOption {
	opts := make([]ui.SelectOption, 0)
	for _, t := range ui.Themes() {
		opts = append(opts, ui.SelectOption{Value: t.Name, Label: t.Label})
	}
	return opts
}

// userAdminHTML is the Add-account panel (top of the Users section). It must
// surface the invite LINK for manual copy when mail isn't configured — which
// the declarative FormPanel can't do — so it rides in a Card. Posts to
// api/users. Reset lives on each user's row (see admin_reset_password).
const userAdminHTML = `<div class="uadm">
  <input id="uadm-add-email" class="uadm-in" type="email" placeholder="email@example.com" autocomplete="off">
  <label class="uadm-chk"><input type="checkbox" id="uadm-add-admin"> Administrator</label>
  <div class="uadm-methods">
    <label><input type="radio" name="uadm-add-method" value="invite" checked> Send registration link</label>
    <label><input type="radio" name="uadm-add-method" value="password"> Set password now</label>
  </div>
  <input id="uadm-add-pw" class="uadm-in" type="password" placeholder="New password (6+ characters)" style="display:none" autocomplete="new-password">
  <div class="uadm-row"><button class="ui-row-btn primary" id="uadm-add-btn">Add account</button><span id="uadm-add-msg" class="uadm-msg"></span></div>
  <div id="uadm-add-link" class="uadm-link" style="display:none"></div>
</div>
<style>
.uadm { display:flex; flex-direction:column; gap:0.5rem; max-width:26rem; }
.uadm-in { background:var(--bg-0); color:var(--text); border:1px solid var(--border); border-radius:6px; padding:0.4rem 0.55rem; font:inherit; font-size:0.9rem; }
.uadm-chk { display:flex; align-items:center; gap:0.4rem; font-size:0.85rem; color:var(--text); }
.uadm-methods { display:flex; flex-direction:column; gap:0.25rem; font-size:0.85rem; color:var(--text-mute); }
.uadm-methods label { display:flex; align-items:center; gap:0.4rem; }
.uadm-row { display:flex; align-items:center; gap:0.6rem; margin-top:0.2rem; }
.uadm-msg { font-size:0.82rem; }
.uadm-msg.ok { color:var(--success); }
.uadm-msg.err { color:var(--danger); }
.uadm-link { border:1px solid var(--accent); border-radius:8px; padding:0.55rem 0.7rem; background:var(--bg-2); display:flex; flex-direction:column; gap:0.35rem; }
.uadm-link-lbl { font-size:0.78rem; color:var(--text-mute); }
.uadm-link code { font-family:ui-monospace,SFMono-Regular,Menlo,monospace; font-size:0.78rem; color:var(--text); word-break:break-all; }
.uadm-link button { align-self:flex-start; }
</style>
<script>
(function(){
  var root = document.querySelector('.uadm');
  if(!root) return;
  function $(id){ return document.getElementById(id); }
  function setMsg(el, t, ok){ el.textContent=t||''; el.className='uadm-msg '+(ok?'ok':'err'); }
  function showLink(box, link, emailed){
    box.innerHTML=''; box.style.display='';
    var lbl=document.createElement('div'); lbl.className='uadm-link-lbl';
    lbl.textContent = emailed ? 'Emailed to the user. Link (copy if needed):' : 'Mail is not configured — copy this link and send it to the user:';
    var code=document.createElement('code'); code.textContent=link;
    var copy=document.createElement('button'); copy.className='ui-row-btn'; copy.textContent='Copy';
    copy.addEventListener('click', function(){ if(navigator.clipboard) navigator.clipboard.writeText(link); copy.textContent='Copied'; setTimeout(function(){ copy.textContent='Copy'; }, 1200); });
    box.appendChild(lbl); box.appendChild(code); box.appendChild(copy);
  }
  var rbs=document.querySelectorAll('input[name="uadm-add-method"]');
  for(var i=0;i<rbs.length;i++){ rbs[i].addEventListener('change', function(){ var s=document.querySelector('input[name="uadm-add-method"]:checked'); $('uadm-add-pw').style.display=(s&&s.value==='password')?'':'none'; }); }
  $('uadm-add-btn').addEventListener('click', function(){
    var email=$('uadm-add-email').value.trim(), msg=$('uadm-add-msg'), linkbox=$('uadm-add-link');
    linkbox.style.display='none'; setMsg(msg,'',true);
    if(!email){ setMsg(msg,'Enter an email.',false); return; }
    var method=(document.querySelector('input[name="uadm-add-method"]:checked')||{}).value;
    var body={username:email, admin:$('uadm-add-admin').checked, invite: method==='invite'};
    if(method==='password'){ var pw=$('uadm-add-pw').value; if(pw.length<6){ setMsg(msg,'Password must be at least 6 characters.',false); return; } body.password=pw; }
    var btn=$('uadm-add-btn'); btn.disabled=true; var orig=btn.textContent; btn.textContent='Working…';
    fetch('api/users',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)})
      .then(function(r){ return r.ok ? r.json() : r.text().then(function(t){ throw new Error(t||('HTTP '+r.status)); }); })
      .then(function(d){
        btn.disabled=false; btn.textContent=orig; $('uadm-add-email').value=''; $('uadm-add-pw').value='';
        if(window.uiInvalidate) window.uiInvalidate('api/users');
        if(d.status==='invited'){ setMsg(msg,'Invite created for '+email+'.',true); showLink(linkbox,d.link,d.emailed); }
        else { setMsg(msg,'Account created for '+email+'.',true); }
      })
      .catch(function(e){ btn.disabled=false; btn.textContent=orig; setMsg(msg,'Failed: '+(e&&e.message||e),false); });
  });
})();
</script>`
