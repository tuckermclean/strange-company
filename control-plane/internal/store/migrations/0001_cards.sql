CREATE TABLE cards (
    id                      uuid PRIMARY KEY,
    vikunja_task_id         bigint UNIQUE,
    title                   text NOT NULL,
    source_type             text NOT NULL,
    source_url              text,
    source_external_id      text,
    repo_url                text,
    repo_base_ref           text,
    branch                  text,
    state                   text NOT NULL,
    phase                   text NOT NULL,
    spec_uri                text,
    plan_uri                text,
    risk_class              text NOT NULL DEFAULT 'R1',
    effective_priority      integer NOT NULL DEFAULT 100,
    claimed_by              text,
    lease_expires_at        timestamptz,
    implementation_attempt  integer NOT NULL DEFAULT 0,
    infrastructure_failures integer NOT NULL DEFAULT 0,
    max_cost_usd            numeric(12,4),
    cost_usd                numeric(12,4) NOT NULL DEFAULT 0,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE acceptance_criteria (
    id           text NOT NULL,
    card_id      uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
    text         text NOT NULL,
    verification text NOT NULL,
    PRIMARY KEY (card_id, id)
);

CREATE TABLE card_dependencies (
    card_id    uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
    depends_on uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
    PRIMARY KEY (card_id, depends_on)
);

CREATE TABLE card_history (
    id         bigserial PRIMARY KEY,
    card_id    uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
    at         timestamptz NOT NULL DEFAULT now(),
    from_state text,
    to_state   text NOT NULL,
    actor_type text NOT NULL,
    actor_id   text NOT NULL,
    reason     text,
    run_id     text,
    evidence   jsonb
);

CREATE INDEX cards_claimable_idx
    ON cards (effective_priority, created_at)
    WHERE state = 'Ready' AND claimed_by IS NULL;
