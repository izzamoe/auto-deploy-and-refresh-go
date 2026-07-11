import { useEffect, useState } from "react";
import { AdminAPIError, getTelegramConfig, saveTelegramConfig } from "./api";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";

export function TelegramSettings({ setFlash }: { setFlash: (flash: { message: string, type: "success" | "error" }) => void }) {
	const [formData, setFormData] = useState({
		app_id: "",
		app_hash: "",
		bot_token: "",
		chat_username: "",
		enabled: false
	});
	const [savedBotToken, setSavedBotToken] = useState("");
	const [errors, setErrors] = useState<string[]>([]);
	const [loading, setLoading] = useState(true);

	useEffect(() => {
		getTelegramConfig().then(cfg => {
			setFormData({
				app_id: cfg.appId ? String(cfg.appId) : "",
				app_hash: cfg.appHash || "",
				bot_token: cfg.botToken || "",
				chat_username: cfg.chatUsername || "",
				enabled: cfg.enabled
			});
			setSavedBotToken(cfg.botToken || "");
			setLoading(false);
		}).catch(err => {
			if (err instanceof AdminAPIError) {
				setFlash({ message: err.message, type: "error" });
			} else {
				setFlash({ message: String(err), type: "error" });
			}
			setLoading(false);
		});
	}, [setFlash]);

	const handleChange = (e: React.ChangeEvent<HTMLInputElement>) => {
		setFormData({ ...formData, [e.target.name]: e.target.value });
	};

	const handleEnabledChange = (enabled: boolean) => {
		setFormData({ ...formData, enabled });
	};

	const handleSubmit = async (e: React.FormEvent) => {
		e.preventDefault();
		setErrors([]);
		try {
			// Only send a new bot token if the user actually typed a different value.
			const botToken = formData.bot_token === savedBotToken ? "" : formData.bot_token;
			const saved = await saveTelegramConfig({
				appId: formData.app_id ? Number(formData.app_id) : 0,
				appHash: formData.app_hash,
				botToken,
				chatUsername: formData.chat_username,
				enabled: formData.enabled
			});
			setFormData({
				app_id: saved.appId ? String(saved.appId) : "",
				app_hash: saved.appHash || "",
				bot_token: saved.botToken || "",
				chat_username: saved.chatUsername || "",
				enabled: saved.enabled
			});
			setSavedBotToken(saved.botToken || "");
			setFlash({ message: "Telegram settings saved successfully", type: "success" });
		} catch (err: unknown) {
			if (err instanceof AdminAPIError) {
				const msgs = Array.isArray(err.errors) && err.errors.length > 0 ? err.errors : [err.message || "An error occurred"];
				setErrors(msgs);
			} else {
				setErrors([String(err)]);
			}
		}
	};

	if (loading) {
		return null;
	}

	return (
		<div className="space-y-6">
			<Card className="border-border/70 bg-card/95 shadow-sm">
				<CardHeader className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
					<div className="space-y-2">
						<CardTitle className="text-3xl font-bold tracking-tight">
							<h1>Telegram Notifications</h1>
						</CardTitle>
						<CardDescription>
							Send a Telegram message whenever a deploy succeeds or fails. Requires a bot token from
							@BotFather plus an App ID / App Hash from my.telegram.org/apps, and only supports
							@username-based chat targets.
						</CardDescription>
					</div>
					<Button variant="outline" asChild>
						<a href="#/">Back to Apps</a>
					</Button>
				</CardHeader>
			</Card>

			{errors.length > 0 && (
				<Alert id="form-errors" variant="destructive">
					<AlertDescription className="space-y-1">
						{errors.map((errStr) => <div key={errStr}>{errStr}</div>)}
					</AlertDescription>
				</Alert>
			)}

			<Card className="border-border/70 bg-card/80 shadow-sm">
				<form id="telegram-form" onSubmit={handleSubmit}>
					<CardContent className="grid gap-5 p-6">
						<div className="grid gap-2 sm:grid-cols-2 sm:gap-4">
							<div className="grid gap-2">
								<Label htmlFor="app_id">App ID</Label>
								<Input type="number" id="app_id" name="app_id" value={formData.app_id} onChange={handleChange} />
							</div>

							<div className="grid gap-2">
								<Label htmlFor="app_hash">App Hash</Label>
								<Input type="text" id="app_hash" name="app_hash" value={formData.app_hash} onChange={handleChange} />
							</div>
						</div>

						<div className="grid gap-2">
							<Label htmlFor="bot_token">Bot Token</Label>
							<Input
								type="password"
								id="bot_token"
								name="bot_token"
								value={formData.bot_token}
								onChange={handleChange}
								placeholder="Leave unchanged to keep current token"
							/>
						</div>

						<div className="grid gap-2">
							<Label htmlFor="chat_username">Chat Username</Label>
							<Input
								type="text"
								id="chat_username"
								name="chat_username"
								value={formData.chat_username}
								onChange={handleChange}
								placeholder="@your_channel_or_user"
							/>
						</div>

						<div className="flex items-center justify-between rounded-lg border bg-background/80 p-4">
							<div className="space-y-0.5">
								<Label htmlFor="enabled">Enable notifications</Label>
								<p className="text-sm text-muted-foreground">
									When disabled, no Telegram messages are sent and the connection is stopped.
								</p>
							</div>
							<Switch
								id="enabled"
								name="enabled"
								checked={formData.enabled}
								onCheckedChange={handleEnabledChange}
								aria-label="Enable notifications"
							/>
						</div>
					</CardContent>

					<CardFooter className="form-actions flex flex-col-reverse items-stretch gap-3 sm:flex-row sm:justify-end">
						<Button variant="outline" asChild>
							<a href="#/">Cancel</a>
						</Button>
						<Button type="submit" id="submit-btn">Save</Button>
					</CardFooter>
				</form>
			</Card>
		</div>
	);
}
