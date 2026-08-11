// Existing compact component remains source-compatible while its capability-aware
// props are progressively applied to the shared Users & Groups page.
// @ts-nocheck
import { FormEvent, useEffect, useState } from "react";
import { hasCapability } from "./capabilities";
import { UserPlus, Users as UsersIcon } from "lucide-react";
import { EmptyState, PageHeader } from "./ui";
import { listOrEmpty } from "./identity-helpers";
import "./identity.css";

type User = {
  id: string;
  username: string;
  display_name: string;
  email: string;
  enabled: boolean;
  is_admin: boolean;
};
type Group = { id: string; name: string; description: string };
const api = async <T,>(path: string, init?: RequestInit): Promise<T> => {
  const r = await fetch(`/api/v1${path}`, {
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
    ...init,
  });
  if (!r.ok) {
    const b = await r.json().catch(() => null);
    throw new Error(b?.error?.message ?? "Request failed");
  }
  return r.status === 204 ? (undefined as T) : r.json();
};
const csrf = (token: string) => ({ "X-CSRF-Token": token });

export function IdentityAdmin({
  token,
  capabilities,
  isAdmin,
}: {
  token: string;
  capabilities?: string[];
  isAdmin: boolean;
}) {
  const [tab, setTab] = useState<"users" | "groups">("users");
  const users =
    hasCapability(capabilities, "Users.View") ||
    hasCapability(capabilities, "Users.Manage");
  const groups =
    hasCapability(capabilities, "Groups.View") ||
    hasCapability(capabilities, "Groups.Manage");
  if (!users && !groups) return null;
  return (
    <section className="identity-page">
      <PageHeader
        eyebrow="Access control"
        title="Users & groups"
        description="Manage local identities and organize access to this GameNode."
      />
      <nav className="segmented-tabs" aria-label="Identity sections">
        {users && (
          <button
            className={tab === "users" ? "active" : ""}
            onClick={() => setTab("users")}
          >
            <UserPlus />
            Users
          </button>
        )}
        {groups && (
          <button
            className={tab === "groups" ? "active" : ""}
            onClick={() => setTab("groups")}
          >
            <UsersIcon />
            Groups
          </button>
        )}
      </nav>
      {tab === "users" && users ? (
        <Users token={token} capabilities={capabilities} isAdmin={isAdmin} />
      ) : (
        <Groups token={token} capabilities={capabilities} />
      )}
    </section>
  );
}
function Users({
  token,
  capabilities,
  isAdmin,
}: {
  token: string;
  capabilities?: string[];
  isAdmin: boolean;
}) {
  const [users, setUsers] = useState<User[]>([]),
    [error, setError] = useState(""),
    [form, setForm] = useState(false);
  const canView = hasCapability(capabilities, "Users.View"),
    canManage = hasCapability(capabilities, "Users.Manage");
  const load = () =>
    canView
      ? api<{ users: User[] | null }>("/users")
          .then((x) => setUsers(listOrEmpty(x.users)))
          .catch((e) => setError(e.message))
      : undefined;
  useEffect(() => {
    void load();
  }, [canView]);
  async function mutate(path: string, method: string, body?: unknown) {
    try {
      await api(path, {
        method,
        headers: csrf(token),
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Request failed");
    }
  }
  return (
    <>
      <div className="row">
        <h2>Local users</h2>
        {canManage && (
          <button onClick={() => setForm(!form)}>
            {form ? "Cancel" : "Create user"}
          </button>
        )}
      </div>
      {form && (
        <UserForm
          token={token}
          onDone={() => {
            setForm(false);
            load();
          }}
          isAdmin={isAdmin}
        />
      )}{" "}
      {!canView && (
        <p className="muted">User directory access is not granted.</p>
      )}
      {error && <p className="error">{error}</p>}
      <div className="server-list">
        {canView && users.length === 0 ? (
          <EmptyState
            title="No users found"
            description="Create a local user to delegate access to this GameNode."
            icon={UserPlus}
          />
        ) : users.map((u) => (
          <section className="panel" key={u.id}>
            <div className="row">
              <div>
                <strong>{u.username}</strong>
                {u.display_name && (
                  <span className="muted"> · {u.display_name}</span>
                )}
                <br />
                <small className="muted">
                  {u.email} · {u.is_admin ? "Administrator" : "User"} ·{" "}
                  {u.enabled ? "Enabled" : "Disabled"}
                </small>
              </div>
              {canManage && (
                <div className="actions">
                  <button
                    className="quiet"
                    onClick={() => {
                      const display_name = prompt(
                        "Display name",
                        u.display_name,
                      );
                      if (display_name !== null)
                        mutate(`/users/${u.id}`, "PATCH", { display_name });
                    }}
                  >
                    Edit
                  </button>
                  <button
                    className="quiet"
                    onClick={() =>
                      mutate(`/users/${u.id}`, "PATCH", { enabled: !u.enabled })
                    }
                  >
                    {u.enabled ? "Disable" : "Enable"}
                  </button>
                  <button
                    className="quiet"
                    onClick={() => {
                      const password = prompt("New password (12+ characters)");
                      if (password)
                        mutate(`/users/${u.id}/password`, "POST", { password });
                    }}
                  >
                    Reset password
                  </button>
                  <button
                    className="danger quiet"
                    onClick={() =>
                      confirm(`Delete ${u.username}?`) &&
                      mutate(`/users/${u.id}`, "DELETE")
                    }
                  >
                    Delete
                  </button>
                </div>
              )}
            </div>
          </section>
        ))}
      </div>
    </>
  );
}
function UserForm({
  token,
  onDone,
  isAdmin,
}: {
  token: string;
  onDone: () => void;
  isAdmin: boolean;
}) {
  const [username, setUsername] = useState(""),
    [displayName, setDisplayName] = useState(""),
    [email, setEmail] = useState(""),
    [password, setPassword] = useState(""),
    [admin, setAdmin] = useState(false),
    [error, setError] = useState("");
  async function submit(e: FormEvent) {
    e.preventDefault();
    try {
      await api("/users", {
        method: "POST",
        headers: csrf(token),
        body: JSON.stringify({
          username,
          display_name: displayName,
          email,
          password,
          is_admin: admin,
        }),
      });
      onDone();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Request failed");
    }
  }
  return (
    <form className="panel" onSubmit={submit}>
      <label>
        Username
        <input
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          required
        />
      </label>
      <label>
        Display name
        <input
          value={displayName}
          onChange={(e) => setDisplayName(e.target.value)}
        />
      </label>
      <label>
        Email
        <input
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          required
        />
      </label>
      <label>
        Temporary password
        <input
          type="password"
          minLength={12}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          required
        />
      </label>
      {isAdmin && (
        <label>
          <input
            type="checkbox"
            checked={admin}
            onChange={(e) => setAdmin(e.target.checked)}
          />{" "}
          Administrator
        </label>
      )}
      {error && <p className="error">{error}</p>}
      <button>Create user</button>
    </form>
  );
}
function Groups({
  token,
  capabilities,
}: {
  token: string;
  capabilities?: string[];
}) {
  const [groups, setGroups] = useState<Group[]>([]),
    [users, setUsers] = useState<User[]>([]),
    [selected, setSelected] = useState<Group>(),
    [members, setMembers] = useState<User[]>([]),
    [name, setName] = useState(""),
    [description, setDescription] = useState(""),
    [error, setError] = useState("");
  const canView = hasCapability(capabilities, "Groups.View"),
    canManage = hasCapability(capabilities, "Groups.Manage"),
    canViewUsers = hasCapability(capabilities, "Users.View");
  const load = () => {
    if (canView)
      api<{ groups: Group[] | null }>("/groups")
        .then((x) => setGroups(listOrEmpty(x.groups)))
        .catch((e) => setError(e.message));
    if (canViewUsers)
      api<{ users: User[] | null }>("/users")
        .then((x) => setUsers(listOrEmpty(x.users)))
        .catch((e) => setError(e.message));
  };
  useEffect(() => {
    void load();
  }, [canView, canViewUsers]);
  const memberLoad = (g: Group) => {
    if (!canView) return;
    setSelected(g);
    api<{ users: User[] | null }>(`/groups/${g.id}/members`)
      .then((x) => setMembers(listOrEmpty(x.users)))
      .catch((e) => setError(e.message));
  };
  async function create(e: FormEvent) {
    e.preventDefault();
    try {
      await api("/groups", {
        method: "POST",
        headers: csrf(token),
        body: JSON.stringify({ name, description }),
      });
      setName("");
      setDescription("");
      load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Request failed");
    }
  }
  async function mutate(path: string, method: string, body?: unknown) {
    try {
      await api(path, {
        method,
        headers: csrf(token),
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      load();
      if (selected) memberLoad(selected);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Request failed");
    }
  }
  return (
    <>
      {canManage && (
        <form className="panel" onSubmit={create}>
          <h2>Create group</h2>
          <label>
            Name
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
            />
          </label>
          <label>
            Description
            <input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </label>
          <button>Create group</button>
        </form>
      )}
      {!canView && (
        <p className="muted">Group directory access is not granted.</p>
      )}
      {error && <p className="error">{error}</p>}
      <div className="server-list">
        {canView && groups.length === 0 ? (
          <EmptyState
            title="No groups yet"
            description="Create a group to organize users and scoped access."
            icon={UsersIcon}
          />
        ) : groups.map((g) => (
          <section className="panel" key={g.id}>
            <div className="row">
              <div>
                <strong>{g.name}</strong>
                <br />
                <small className="muted">{g.description}</small>
              </div>
              <div className="actions">
                {canView && (
                  <button className="quiet" onClick={() => memberLoad(g)}>
                    Members
                  </button>
                )}
                {canManage && (
                  <>
                    <button
                      className="quiet"
                      onClick={() => {
                        const name = prompt("Group name", g.name);
                        if (name !== null)
                          mutate(`/groups/${g.id}`, "PATCH", { name });
                      }}
                    >
                      Rename
                    </button>
                    <button
                      className="danger quiet"
                      onClick={() =>
                        confirm(`Delete ${g.name}?`) &&
                        mutate(`/groups/${g.id}`, "DELETE")
                      }
                    >
                      Delete
                    </button>
                  </>
                )}
              </div>
            </div>
          </section>
        ))}
      </div>
      {selected && canView && (
        <section className="panel">
          <div className="row">
            <h2>{selected.name} members</h2>
            <button className="quiet" onClick={() => setSelected(undefined)}>
              Close
            </button>
          </div>
          {canManage && canViewUsers && (
            <select
              defaultValue=""
              onChange={(e) => {
                if (e.target.value)
                  mutate(`/groups/${selected.id}/members`, "POST", {
                    user_id: e.target.value,
                  });
              }}
            >
              <option value="">Add user…</option>
              {users
                .filter((u) => !members.some((m) => m.id === u.id))
                .map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.username}
                  </option>
                ))}
            </select>
          )}
          {members.map((u) => (
            <p key={u.id}>
              {u.username}{" "}
              {canManage && (
                <button
                  className="danger quiet"
                  onClick={() =>
                    mutate(`/groups/${selected.id}/members/${u.id}`, "DELETE")
                  }
                >
                  Remove
                </button>
              )}
            </p>
          ))}
        </section>
      )}
    </>
  );
}
