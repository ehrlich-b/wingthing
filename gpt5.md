# WingThing.ai — “two wings to fly” for real-world support (not therapy)

## Executive summary

WingThing is a **support companion** that does three things obsessively well:

1. **Holds a durable, high-fidelity model of you** (memory/persona with boundaries).
2. **Sleeps on it** every night (“Dreams”) to synthesize the day into a **tomorrow plan** (lightweight RAG + action cues).
3. **Finds and fortifies your second wing**—structured social features that help you cultivate **human** support (friends, co-parents, neighbors, micro-pods).

Positioning: unlike AI “companions” that aim to be *the* relationship (Replika, Character.AI, Nomi, Pi), WingThing is **explicitly pro-human-connection**: the bot is the *bionic wing* that helps you fly while it helps you attach the other wing in real life. Relevance is high: the U.S. Surgeon General has called out loneliness/isolation as a public-health crisis—WingThing’s goal is to measurably increase social connection *without* practicing therapy or claiming clinical outcomes. ([HHS][1])

---

## Competitive landscape (what’s out there)

* **Replika** – long-running AI “friend/partner,” heavy on companionship/romance, large user base. ([Business Insider][2])
* **Character.AI** – character role-play + group chats/rooms; recently under scrutiny and *tightening access for minors* (signal: mounting safety pressure in the category). ([TechCrunch][3])
* **Pi (Inflection)** – “personal AI…supportive, smart, there anytime” (empathetic tone; generalist companion). ([Pi][4])
* **Nomi** – markets “AI companion with memory & a soul,” heavy emphasis on persistent memory/backstory tools (Shared Notes, Identity Core). ([Nomi.ai][5])
* **Therapeutic chatbots (Wysa, Woebot)** – explicitly clinical; Wysa has **FDA Breakthrough Device** designation; Woebot has RCTs. These are *medical/therapeutic* and sit in a regulated lane that you want to avoid emulating in claims. ([wysa.com][6])
* **Relationship CRMs (Clay, Notion templates, etc.)** – tools that prompt follow-ups and remember dates; strong *utilities* but not a companion. ([Clay][7])
* **Voice empathy stacks (Hume EVI)** – real-time prosody/emotion sensing with fast TTS; relevant for your voice UX but again not a companion by itself. ([Hume AI][8])
* **Journaling assistants (Apple Journal)** – on-device suggestions & privacy posture; demonstrates mainstream demand for *gentle, private reflection nudges*. ([Apple][9])

**Takeaway:** The field splits into (a) parasocial companions, (b) clinical chatbots, and (c) utilities. There’s clear white-space for a **“human-first, anti-parasocial, action-biased support system”** that *engineers real connection* and helps busy people execute micro-acts of care.

---

## Product pillars that differentiate WingThing

### 1) The **Persona Vault** (memory you can audit)

* **Memory graph**: people in your life, routines, constraints, preferences, triggers, boundaries, and “care opportunities” (e.g., “text Alex before his big meeting Tuesday”).
* **User-visible controls**: a **Memory Ledger** that shows new entries/edits, with accept/merge/forget toggles and **red-line rules** (e.g., “never bring up X,” “avoid parenting advice”).
* **Bounded empathy**: tonal calibration profiles (“steady co-pilot,” “light, dry humor,” “no pep talks before 7am”).
* **Local-first mode** (if feasible in v2) + E2EE cloud backup; privacy posture inspired by Apple Journal’s “on-device suggestions” narrative. ([App Store][10])

### 2) Nightly **Dreams**

* **Ingest**: calendar, notes, (optional) private journaling, completed tasks, short voice debrief.
* **Synthesize** with RAG:

  * **Yesterday in 8 bullets** (facts, not vibes),
  * **Open loops** (2–5 items that realistically matter),
  * **Support plan for tomorrow**: 3 tiny moves that increase connection (e.g., draft a 30-second check-in text; schedule a 10-min “wind-down call”; prep a 2-line Ask for help).
* **Output**: a **Morning Card** + **Just-in-time nudges** tied to your day’s schedule.
* **Safety**: no mood scoring, no diagnostics; no claims of symptom relief.

### 3) **Two Wings** features (engineered human connection)

* **WingPair**: opt-in matching to a *real* wing (neighbor parent, co-worker buddy, “new-dad pod,” “Sunday reset pod”).
* **Micro-commitments**: 10–20 min weekly call; a standing “bat signal” check-in emoji; one real-life favor/month.
* **Wing Concierge**: the bot drafts the first message, proposes time slots, adds calendar invites, and sets “default topics” to reduce awkwardness.
* **Pods & Rotations**: Character.AI popularized group rooms for multiple AIs; WingThing’s variant is **human-only** or human-first pods with opt-in assistant facilitation (agenda, timekeeping, action recap). ([TechCrunch][3])
* **Anti-parasocial guardrails**: frequent “hand-off to human” nudges; KPI is **H2H minutes created**, not time-in-app.

---

## MVP blueprint (6–10 weeks of honest scope)

**Core loop/day 0 → day 7**

1. **Onboarding**: persona scaffolding (relationships, constraints, boundaries), tone profile, and time windows for pings.
2. **Morning Card** (from last night’s Dreams): 3 action cues + 20-sec voice summary.
3. **Micro-nudge delivery** at context-relevant moments (calendar-aware).
4. **WingPair waitlist**: collect matching vectors (life stage, time zone, cadence, expectations).
5. **Memory Ledger** UI with “accept/merge/forget” and a one-tap “teach” affordance.

**Nice-to-haves for v1.1**: lightweight voice (EVI-style prosody would be a step change), SMS relay for one-taps, and a tiny “Pod” template (30-min weekly format). ([Hume AI][11])

---

## System architecture (concrete)

* **Clients**: mobile first; push-centric UX.
* **Services**

  * **Event ingester** (calendar, notes, voice, journaling).
  * **Memory service** with types: `fact | routine | relationship | boundary | preference | commitment | opportunity`.
  * **Vector store** for long-term retrieval + **symbolic KV graph** for hard facts/boundaries.
  * **Dreams job** (nightly):

    * compile → dedupe → write Memory Deltas → draft Morning Card → schedule nudges.
  * **Nudge scheduler** aligned to calendar and quiet hours.
  * **WingPair matcher** (privacy-preserving embeddings + rule filters).
* **Safety**

  * **Policy classifier** (content, crisis, minors).
  * **Hard refusals** on therapy/diagnosis; safe-harbor redirects to *external* resources.
* **Privacy**

  * E2EE for memory export/backup, narrow scopes, on-device redaction where possible.
  * **Therapy-avoidance**: no PHQ-9/GAD-7, no symptom claims; learn from clinical bots (Wysa/Woebot) that crossing into *treatment* lands you in medical-device territory. ([wysa.com][6])

---

## UX flows (busy-parent friendly)

* **60-second nightly voice debrief** → Dreams → **90-second Morning Card**.
* **One-tap social actions**:

  * “Send this check-in to Jess” (edits before send).
  * “Offer a swap: I do pickup Tue if you do Thu?” (drafted, scheduled).
  * “Start a 3-person Sunday reset pod” (Wing Concierge drafts invites and agenda).
* **Boundary surfacing**: “You said: no advice at night; I’ll hold this for the morning.”
* **Ledger prompt**: “I noticed you rescheduled workouts 3×; archive ‘Tue 6am gym’ as unrealistic?”

---

## Safety, minors, and reputational risk

The category is moving fast toward **stricter youth guardrails** (see Character.AI’s phased ban for under-18s). For WingThing, make “adults-only” and “not therapy” a core identity from day one, with documented escalation pathways and *no* clinical claims. ([The Verge][12])

---

## Metrics that matter (avoid vanity)

* **Connectedness Score** (leading indicator): H2H minutes WingThing catalyzed/week; # distinct humans contacted; # in-person micro-events.
* **Execution Rate**: % of Morning Card actions completed.
* **Stickiness**: Days Active / Week, but **cap** target to avoid parasocial drift.
* **Second-Wing Adoption**: % users matched + ≥4 weeks retained in a pod or pairing.
* **Privacy Trust**: opt-in rate for data sources; Ledger accept/decline ratio.
* **Safety**: crisis flags routed; zero therapy-claim violations.

---

## Business model options

* **Subscription**: $10–15/mo individual; **Family** bundle (two adults).
* **Employer/EAP-adjacent** but **non-clinical**: positioned as a *connection & caregiving support tool*, not mental health.
* **Local pods**: optional paid, moderated communities (cost covers curation, not content).

---

## “What makes this *not* another AI companion?”

1. **Human-first KPI** (H2H minutes), not chat time.
2. **Action bias** (drafts, schedules, holds you lightly accountable).
3. **Auditable memory** with explicit user control.
4. **Boundary-aware tone** (no nightly pep talks if you said “don’t”).
5. **Two Wings** network effects are **offline** and **small-group** by design.
6. **Therapy-avoidant product/legal posture** (no diagnostics, no “treatment efficacy” claims; point to pro help when needed, like clinical bots do in their own lanes). ([wysa.com][6])

---

## Concrete artifacts you can ship first

* **Morning Card spec** (JSON):

```json
{
  "yesterday": ["..."],
  "open_loops": ["..."],
  "support_plan": [
    {"action":"text_check_in","target":"Jess","draft":"How’s the move going? Want me to grab Aiden Thu?"},
    {"action":"prep10","target":"self","draft":"Lay out daycare bags tonight, set coffee timer"},
    {"action":"pod_invite","target":["Alex","Mo"],"draft":"Sun 8:30p 20-min reset? Topics auto-added."}
  ],
  "quiet_hours":"20:30-07:00",
  "tone":"steady_copilot"
}
```

* **Memory Ledger entries**:

```json
{"type":"boundary","rule":"no_nighttime_advice"}
{"type":"relationship","person":"Jess","note":"moving week; offer school pickup swap"}
{"type":"commitment","item":"Thu pickup swap","status":"proposed"}
```

* **Wing Concierge**: a single endpoint that takes intent (`check_in | swap | invite`) and returns three editable message drafts + 2–3 calendar slots.

---

## Risks & mitigations

* **Parasocial dependence** → hard caps on daily chat minutes; default “hand-off to human” nudges.
* **Privacy headlines** → local-first mode, transparent Ledger, on-device processing where possible (cf. Apple Journal’s privacy story). ([Apple][9])
* **Regulatory drift** → clear *not therapy* copy and escalation pathways (contrast with Wysa/Woebot’s clinical track). ([wysa.com][6])
* **Youth access** → age-gating from day one (industry moving that way). ([The Verge][12])

---

## Closing thought

You’ll win by **owning the “bionic wing” lane**: a meticulous memory + nightly synthesis + social orchestration that converts busy, fragmented days into **small, compounding acts of connection**—while resisting the gravity of therapy claims and parasocial design.

If you want, I can turn this into a 1-pager spec (problem, user stories, KPIs, v1 schema, trust & safety playbook) and a 2-sprint MVP plan.

[1]: https://www.hhs.gov/sites/default/files/surgeon-general-social-connection-advisory.pdf?utm_source=chatgpt.com "Our Epidemic of Loneliness and Isolation"
[2]: https://www.businessinsider.com/replika-ceo-eugenia-kuyda-launch-wabi-2025-10?utm_source=chatgpt.com "The CEO of 'AI companion' startup Replika is stepping aside to launch a new company"
[3]: https://techcrunch.com/2023/10/11/character-ai-introduces-group-chats-where-people-and-multiple-ais-can-talk-to-each-other/?utm_source=chatgpt.com "Character.AI introduces group chats where people and ..."
[4]: https://pi.ai/?utm_source=chatgpt.com "Pi, your personal AI"
[5]: https://nomi.ai/?utm_source=chatgpt.com "Nomi.ai – AI Companion, Girlfriend, Boyfriend, Friend with a ..."
[6]: https://www.wysa.com/clinical-evidence?utm_source=chatgpt.com "Wysa Clinical Evidence & Research"
[7]: https://clay.earth/?utm_source=chatgpt.com "Clay - Be more thoughtful with the people in your network."
[8]: https://dev.hume.ai/docs/empathic-voice-interface-evi/overview?utm_source=chatgpt.com "Empathic Voice Interface (EVI) - Hume API"
[9]: https://www.apple.com/newsroom/2023/12/apple-launches-journal-app-a-new-app-for-reflecting-on-everyday-moments/?utm_source=chatgpt.com "Apple launches Journal, a new app to reflect on everyday ..."
[10]: https://apps.apple.com/us/app/journal/id6447391597?utm_source=chatgpt.com "Journal - App Store - Apple"
[11]: https://www.hume.ai/empathic-voice-interface?utm_source=chatgpt.com "Empathic Voice Interface"
[12]: https://www.theverge.com/ai-artificial-intelligence/808081/character-ai-under-18-chat-ban?utm_source=chatgpt.com "Character.AI is banning minors from AI character chats"
