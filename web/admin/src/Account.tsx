import { useState } from "react";
import { AdminAPIError, changePassword, changeUsername } from "./api";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

function errorMessage(err: unknown): string {
  if (err instanceof AdminAPIError) return err.message;
  return String(err);
}

/** ForcePasswordChange is the blocking screen shown while the seeded default
 * account (admin/11) has not yet chosen a new password. */
export function ForcePasswordChange({ onDone }: { onDone: () => void }) {
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    if (newPassword !== confirm) {
      setError("New password and confirmation do not match.");
      return;
    }
    setBusy(true);
    try {
      await changePassword(currentPassword, newPassword);
      onDone();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="min-h-screen bg-background flex items-center justify-center p-6 font-sans">
      <Card className="w-full max-w-md" data-testid="force-change">
        <CardHeader>
          <CardTitle>Change your password</CardTitle>
          <CardDescription>
            This account still uses the default password. Choose a new one before continuing.
          </CardDescription>
        </CardHeader>
        <form onSubmit={submit}>
          <CardContent className="space-y-4">
            {error && (
              <Alert variant="destructive">
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
            <div className="space-y-2">
              <Label htmlFor="fc-current">Current password</Label>
              <Input id="fc-current" type="password" value={currentPassword} autoComplete="current-password"
                onChange={(e) => setCurrentPassword(e.target.value)} required />
            </div>
            <div className="space-y-2">
              <Label htmlFor="fc-new">New password</Label>
              <Input id="fc-new" type="password" value={newPassword} autoComplete="new-password"
                onChange={(e) => setNewPassword(e.target.value)} required />
            </div>
            <div className="space-y-2">
              <Label htmlFor="fc-confirm">Confirm new password</Label>
              <Input id="fc-confirm" type="password" value={confirm} autoComplete="new-password"
                onChange={(e) => setConfirm(e.target.value)} required />
            </div>
          </CardContent>
          <CardFooter>
            <Button type="submit" disabled={busy} className="w-full">
              {busy ? "Saving…" : "Set new password"}
            </Button>
          </CardFooter>
        </form>
      </Card>
    </div>
  );
}

/** AccountSettings lets an authenticated admin change their username and password. */
export function AccountSettings({ username, onUpdated, setFlash }: {
  username: string;
  onUpdated: () => void;
  setFlash: (flash: { message: string; type: "success" | "error" }) => void;
}) {
  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">Account Settings</h1>
        <p className="text-muted-foreground text-sm">Signed in as <strong>{username}</strong>.</p>
      </div>
      <ChangePasswordCard setFlash={setFlash} />
      <ChangeUsernameCard onUpdated={onUpdated} setFlash={setFlash} />
    </div>
  );
}

function ChangePasswordCard({ setFlash }: { setFlash: (f: { message: string; type: "success" | "error" }) => void }) {
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      await changePassword(currentPassword, newPassword);
      setFlash({ message: "Password updated.", type: "success" });
      setCurrentPassword("");
      setNewPassword("");
    } catch (err) {
      setFlash({ message: errorMessage(err), type: "error" });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>Change password</CardTitle>
      </CardHeader>
      <form onSubmit={submit}>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="cp-current">Current password</Label>
            <Input id="cp-current" type="password" value={currentPassword} autoComplete="current-password"
              onChange={(e) => setCurrentPassword(e.target.value)} required />
          </div>
          <div className="space-y-2">
            <Label htmlFor="cp-new">New password</Label>
            <Input id="cp-new" type="password" value={newPassword} autoComplete="new-password"
              onChange={(e) => setNewPassword(e.target.value)} required />
          </div>
        </CardContent>
        <CardFooter>
          <Button type="submit" disabled={busy}>{busy ? "Saving…" : "Update password"}</Button>
        </CardFooter>
      </form>
    </Card>
  );
}

function ChangeUsernameCard({ onUpdated, setFlash }: {
  onUpdated: () => void;
  setFlash: (f: { message: string; type: "success" | "error" }) => void;
}) {
  const [currentPassword, setCurrentPassword] = useState("");
  const [newUsername, setNewUsername] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      await changeUsername(currentPassword, newUsername);
      setFlash({ message: "Username updated.", type: "success" });
      setCurrentPassword("");
      setNewUsername("");
      onUpdated();
    } catch (err) {
      setFlash({ message: errorMessage(err), type: "error" });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>Change username</CardTitle>
      </CardHeader>
      <form onSubmit={submit}>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="cu-new">New username</Label>
            <Input id="cu-new" value={newUsername}
              onChange={(e) => setNewUsername(e.target.value)} required />
          </div>
          <div className="space-y-2">
            <Label htmlFor="cu-current">Current password</Label>
            <Input id="cu-current" type="password" value={currentPassword} autoComplete="current-password"
              onChange={(e) => setCurrentPassword(e.target.value)} required />
          </div>
        </CardContent>
        <CardFooter>
          <Button type="submit" disabled={busy}>{busy ? "Saving…" : "Update username"}</Button>
        </CardFooter>
      </form>
    </Card>
  );
}
