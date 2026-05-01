import { useEffect, useState } from "react";
import { AdminAPIError, apiRequest } from "./api";

export function AppForm({ id, navigate, setFlash }: { id?: string; navigate: (hash: string, flashMessage?: string) => void; setFlash: (flash: { message: string, type: "success" | "error" }) => void }) {
	const [formData, setFormData] = useState({
		name: "",
		webhook_secret: "",
		service_name: "",
		binary_path: "",
		github_repo: "",
		artifact_name: "",
		enabled: true
	});
	const [errors, setErrors] = useState<string[]>([]);

	useEffect(() => {
		if (id) {
			apiRequest(`/apps/${id}`).then(res => {
				setFormData({
					name: res.app.name,
					webhook_secret: "",
					service_name: res.app.serviceName,
					binary_path: res.app.binaryPath,
					github_repo: res.app.githubRepo,
					artifact_name: res.app.artifactName,
					enabled: res.app.enabled
				});
			}).catch(err => {
				if (err instanceof AdminAPIError) {
					setFlash({ message: err.message, type: "error" });
				} else {
					setFlash({ message: String(err), type: "error" });
				}
				navigate("#/");
			});
		}
	}, [id, navigate, setFlash]);

	const handleChange = (e: React.ChangeEvent<HTMLInputElement>) => {
		setFormData({ ...formData, [e.target.name]: e.target.value });
	};

	const handleSubmit = async (e: React.FormEvent) => {
		e.preventDefault();
		setErrors([]);
		try {
			const body = {
				name: formData.name,
				webhookSecret: formData.webhook_secret,
				serviceName: formData.service_name,
				binaryPath: formData.binary_path,
				githubRepo: formData.github_repo,
				artifactName: formData.artifact_name,
				enabled: formData.enabled
			};
			
			if (id) {
				await apiRequest(`/apps/${id}`, {
					method: "PUT",
					body: JSON.stringify(body)
				});
				navigate("#/", "App updated successfully");
			} else {
				await apiRequest(`/apps`, {
					method: "POST",
					body: JSON.stringify(body)
				});
				navigate("#/", "App created successfully");
			}
		} catch (err: unknown) {
			if (err instanceof AdminAPIError) {
				const msgs = Array.isArray(err.errors) && err.errors.length > 0 ? err.errors : [err.message || "An error occurred"];
				setErrors(msgs);
			} else {
				setErrors([String(err)]);
			}
		}
	};

	return (
		<div>
			<div className="form-header">
				<h1>{id ? "Edit App" : "New App"}</h1>
				<a href="#/">Back to Apps</a>
			</div>
			
			{errors.length > 0 && (
				<div id="form-errors" className="errors">
					{errors.map((errStr) => <div key={errStr}>{errStr}</div>)}
				</div>
			)}
			
			<form id="app-form" onSubmit={handleSubmit}>
				<div>
					<label htmlFor="name">App Name</label>
					<input type="text" id="name" name="name" value={formData.name} onChange={handleChange} required />
				</div>
				
				<div>
					<label htmlFor="webhook_secret">Webhook Secret</label>
					<input 
						type="password" 
						id="webhook_secret" 
						name="webhook_secret" 
						value={formData.webhook_secret} 
						onChange={handleChange} 
						required={!id} 
						placeholder={id ? "Leave blank to keep current secret" : ""}
					/>
				</div>
				
				<div>
					<label htmlFor="service_name">Service Name</label>
					<input type="text" id="service_name" name="service_name" value={formData.service_name} onChange={handleChange} required />
				</div>
				
				<div>
					<label htmlFor="binary_path">Binary Path</label>
					<input type="text" id="binary_path" name="binary_path" value={formData.binary_path} onChange={handleChange} required />
				</div>
				
				<div>
					<label htmlFor="github_repo">GitHub Repo</label>
					<input type="text" id="github_repo" name="github_repo" value={formData.github_repo} onChange={handleChange} required />
				</div>
				
				<div>
					<label htmlFor="artifact_name">Artifact Name</label>
					<input type="text" id="artifact_name" name="artifact_name" value={formData.artifact_name} onChange={handleChange} required />
				</div>

				<div className="form-actions">
					<button type="submit" id="submit-btn">{id ? "Update App" : "Create App"}</button>
					<a href="#/">Cancel</a>
				</div>
			</form>
		</div>
	);
}
