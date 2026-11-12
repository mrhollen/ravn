// Artifact attachment + preview surface extracted from web/src/app.ts
// (Phase 2 batch C). Plain TS factory, no Alpine state of its own: every bound
// name stays a flat plain root field and is reached through ArtifactsDeps —
// 13 state-in getters, 11 state-out setters (incl. setDetail for the artifact
// list patch), 4 hooks (toast/actor/isWebHref/confirmDiscard). Bodies moved
// verbatim except `this.` → closure renames; whole-field writes (artifactForm,
// previewModal, the text-cache Records) go through setters, while in-place
// property writes (artifactForm.file/.kind, previewModal.open, detail.artifacts
// reassignment) happen on the shared objects through the state-IN getters.

import { api, errMsg } from "../api";
import type { ActorInput, ArtifactKind, ArtifactRow, TaskDetail } from "../types";
import { copyTextToClipboard, isTextArtifact } from "../util";

export interface ArtifactFormState {
  open: boolean;
  kind: ArtifactKind;
  ref: string;
  summary: string;
  file: File | null;
}

export interface PreviewModalState {
  open: boolean;
  src: string;
  alt: string;
}

export interface ArtifactsDeps {
  // state-in
  detail: () => TaskDetail | null;
  artifactForm: () => ArtifactFormState;
  attachingArtifact: () => boolean;
  previewModal: () => PreviewModalState;
  copiedArtifactId: () => string | null;
  copiedArtifactTimer: () => ReturnType<typeof setTimeout> | null;
  deletingArtifactId: () => string;
  artifactTextExpanded: () => Record<string, boolean>;
  artifactTextContent: () => Record<string, string>;
  artifactTextLoading: () => Record<string, boolean>;
  artifactTextError: () => Record<string, string>;
  artifactDragOver: () => boolean;
  detailOpen: () => boolean;
  // state-out
  setDetail: (d: TaskDetail) => void;
  setArtifactForm: (f: ArtifactFormState) => void;
  setAttachingArtifact: (v: boolean) => void;
  setPreviewModal: (p: PreviewModalState) => void;
  setCopiedArtifactId: (id: string | null) => void;
  setCopiedArtifactTimer: (t: ReturnType<typeof setTimeout> | null) => void;
  setDeletingArtifactId: (v: string) => void;
  setArtifactTextExpanded: (r: Record<string, boolean>) => void;
  setArtifactTextContent: (r: Record<string, string>) => void;
  setArtifactTextLoading: (r: Record<string, boolean>) => void;
  setArtifactTextError: (r: Record<string, string>) => void;
  setArtifactDragOver: (v: boolean) => void;
  // hooks
  toast: (msg: string, kind?: "error" | "info") => number;
  actor: () => ActorInput;
  isWebHref: (s: string | null | undefined) => boolean;
  confirmDiscard: (message: string) => boolean;
}

export function createArtifactsModule(deps: ArtifactsDeps) {
  function onArtifactFileSelected(event: Event): void {
    const target = event.target as HTMLInputElement;
    const file = target.files?.[0] || null;
    deps.artifactForm().file = file;
    if (file) {
      if (file.type.startsWith("image/") || /\.(png|jpe?g|gif|webp|svg|bmp|avif)$/i.test(file.name)) {
        deps.artifactForm().kind = "screenshot";
      } else {
        deps.artifactForm().kind = "other";
      }
    }
  }

  async function uploadFileArtifact(file: File, summary?: string, kind?: ArtifactKind): Promise<ArtifactRow | null> {
    const t = deps.detail();
    if (!t || deps.attachingArtifact()) return null;
    deps.setAttachingArtifact(true);
    try {
      let inferredKind: ArtifactKind = kind || "other";
      if (!kind) {
        if (file.type.startsWith("image/") || /\.(png|jpe?g|gif|webp|svg|bmp|avif)$/i.test(file.name)) {
          inferredKind = "screenshot";
        } else if (file.type.includes("text") || file.name.endsWith(".log")) {
          inferredKind = "log";
        }
      }
      const a = await api.uploadArtifact(
        t.id,
        file,
        inferredKind,
        summary || file.name,
        deps.actor(),
      );
      const d = deps.detail();
      if (d && d.id === t.id) deps.setDetail({ ...d, artifacts: [...d.artifacts, a] });
      deps.toast(`Attached ${a.kind}: ${file.name}`);
      return a;
    } catch (e) {
      deps.toast(errMsg(e), "error");
      return null;
    } finally {
      deps.setAttachingArtifact(false);
    }
  }

  async function handleArtifactDrop(e: DragEvent): Promise<void> {
    deps.setArtifactDragOver(false);
    const files = e.dataTransfer?.files;
    if (!files || files.length === 0) return;
    for (let i = 0; i < files.length; i++) {
      await uploadFileArtifact(files[i]);
    }
  }

  async function handlePaste(e: ClipboardEvent): Promise<void> {
    if (!deps.detailOpen() || !deps.detail()) return;
    const items = e.clipboardData?.items;
    if (!items) return;
    for (let i = 0; i < items.length; i++) {
      const item = items[i];
      if (item.kind === "file" && item.type.startsWith("image/")) {
        e.preventDefault();
        const file = item.getAsFile();
        if (!file) continue;
        const timestamp = new Date().toISOString().replace(/[:.]/g, "-");
        const ext = item.type.split("/")[1] || "png";
        const filename = file.name && file.name !== "image.png" ? file.name : `screenshot-${timestamp}.${ext}`;
        const namedFile = new File([file], filename, {
          type: item.type,
        });
        const a = await uploadFileArtifact(namedFile, filename, "screenshot");
        const target = e.target as HTMLTextAreaElement | null;
        if (target && target.tagName === "TEXTAREA" && a) {
          const mdRef = `\n![screenshot](${a.ref})\n`;
          const start = target.selectionStart ?? target.value.length;
          const end = target.selectionEnd ?? target.value.length;
          const val = target.value;
          target.value = val.slice(0, start) + mdRef + val.slice(end);
          target.selectionStart = target.selectionEnd = start + mdRef.length;
          target.dispatchEvent(new Event("input", { bubbles: true }));
        }
        break;
      }
    }
  }

  function resetArtifactForm(): void {
    deps.setArtifactForm({ open: false, kind: "url", ref: "", summary: "", file: null });
    const fileInput = document.getElementById("artifactFileInput") as HTMLInputElement | null;
    if (fileInput) fileInput.value = "";
  }

  async function attachArtifact(): Promise<void> {
    const t = deps.detail();
    const file = deps.artifactForm().file;
    const ref = deps.artifactForm().ref.trim();
    if (!t || (!file && !ref) || deps.attachingArtifact()) return;
    deps.setAttachingArtifact(true);
    try {
      let a: ArtifactRow;
      if (file) {
        a = await api.uploadArtifact(
          t.id,
          file,
          deps.artifactForm().kind,
          deps.artifactForm().summary.trim() || undefined,
          deps.actor(),
        );
      } else {
        a = await api.addArtifact(
          t.id,
          deps.artifactForm().kind,
          ref,
          deps.artifactForm().summary.trim() || undefined,
          deps.actor(),
        );
      }
      const d = deps.detail();
      if (d && d.id === t.id) deps.setDetail({ ...d, artifacts: [...d.artifacts, a] });
      resetArtifactForm();
    } catch (e) {
      deps.toast(errMsg(e), "error");
    } finally {
      deps.setAttachingArtifact(false);
    }
  }

  function openImagePreview(src: string, alt: string): void {
    deps.setPreviewModal({ open: true, src, alt });
    const dialog = document.getElementById("artifactPreviewDialog") as HTMLDialogElement | null;
    if (dialog && typeof dialog.showModal === "function") {
      if (!dialog.open) {
        dialog.showModal();
      }
    }
  }

  function closeImagePreview(): void {
    deps.previewModal().open = false;
    const dialog = document.getElementById("artifactPreviewDialog") as HTMLDialogElement | null;
    if (dialog && dialog.open && typeof dialog.close === "function") {
      dialog.close();
    }
  }

  function isStoredArtifact(ref: string | null | undefined): boolean {
    return typeof ref === "string" && (ref.startsWith("/artifacts/") || ref.startsWith("/api/v1/artifacts/"));
  }

  function isStoredImage(a: { kind: string; ref: string; summary?: string }): boolean {
    if (!isStoredArtifact(a.ref)) return false;
    if (a.kind === "screenshot") return true;
    const imageExt = /\.(png|jpe?g|gif|webp|svg|bmp|avif)\b/i;
    return imageExt.test(a.ref) || imageExt.test(a.summary || "");
  }

  function isStoredVideo(a: { kind: string; ref: string; summary?: string }): boolean {
    if (!isStoredArtifact(a.ref)) return false;
    const videoExt = /\.(mp4|webm|ogg|mov)\b/i;
    return videoExt.test(a.ref) || videoExt.test(a.summary || "");
  }

  function isStoredAudio(a: { kind: string; ref: string; summary?: string }): boolean {
    if (!isStoredArtifact(a.ref)) return false;
    const audioExt = /\.(mp3|wav|ogg|aac|flac|m4a)\b/i;
    return audioExt.test(a.ref) || audioExt.test(a.summary || "");
  }

  function isStoredNonImage(a: { kind: string; ref: string; summary?: string }): boolean {
    return isStoredArtifact(a.ref) && !isStoredImage(a) && !isStoredVideo(a) && !isStoredAudio(a);
  }

  function isStoredText(a: { kind: string; ref: string; summary?: string }): boolean {
    if (!a || !a.ref) return false;
    const isFetchable = isStoredArtifact(a.ref) || deps.isWebHref(a.ref);
    return Boolean(isFetchable && isTextArtifact(a));
  }

  async function toggleArtifactPreview(a: { id: string; ref: string }): Promise<void> {
    const current = Boolean(deps.artifactTextExpanded()[a.id]);
    deps.setArtifactTextExpanded({
      ...deps.artifactTextExpanded(),
      [a.id]: !current,
    });
    if (!current && deps.artifactTextContent()[a.id] === undefined && !deps.artifactTextLoading()[a.id]) {
      deps.setArtifactTextLoading({ ...deps.artifactTextLoading(), [a.id]: true });
      deps.setArtifactTextError({ ...deps.artifactTextError(), [a.id]: "" });
      try {
        const res = await fetch(a.ref);
        if (!res.ok) {
          throw new Error(`HTTP ${res.status} ${res.statusText}`);
        }
        const text = await res.text();
        deps.setArtifactTextContent({ ...deps.artifactTextContent(), [a.id]: text });
      } catch (err: any) {
        deps.setArtifactTextError({
          ...deps.artifactTextError(),
          [a.id]: err?.message || "Failed to load artifact content",
        });
      } finally {
        deps.setArtifactTextLoading({ ...deps.artifactTextLoading(), [a.id]: false });
      }
    }
  }

  async function deleteArtifact(artifactId: string): Promise<void> {
    const t = deps.detail();
    if (!t || deps.deletingArtifactId()) return;
    if (!deps.confirmDiscard("Are you sure you want to delete this artifact?")) return;
    deps.setDeletingArtifactId(artifactId);
    try {
      await api.deleteArtifact(t.id, artifactId, deps.actor());
      const d = deps.detail();
      if (d && d.id === t.id) {
        deps.setDetail({
          ...d,
          artifacts: d.artifacts.filter((a) => a.id !== artifactId),
        });
      }
      const nextExpanded = { ...deps.artifactTextExpanded() };
      delete nextExpanded[artifactId];
      deps.setArtifactTextExpanded(nextExpanded);
      const nextContent = { ...deps.artifactTextContent() };
      delete nextContent[artifactId];
      deps.setArtifactTextContent(nextContent);
      deps.toast("Artifact deleted");
    } catch (e) {
      deps.toast(errMsg(e), "error");
    } finally {
      deps.setDeletingArtifactId("");
    }
  }

  /** Copy an artifact ref (file path or URL) to clipboard with brief visual confirmation. */
  async function copyArtifactRef(ref: string, id?: string): Promise<void> {
    if (!ref) return;
    const copied = await copyTextToClipboard(ref);
    if (copied) {
      if (id) {
        deps.setCopiedArtifactId(id);
        if (deps.copiedArtifactTimer()) clearTimeout(deps.copiedArtifactTimer()!);
        deps.setCopiedArtifactTimer(setTimeout(() => {
          if (deps.copiedArtifactId() === id) deps.setCopiedArtifactId(null);
        }, 2000));
      }
      deps.toast("Copied artifact path to clipboard", "info");
    } else {
      deps.toast("Failed to copy artifact path", "error");
    }
  }

  return {
    onArtifactFileSelected,
    uploadFileArtifact,
    handleArtifactDrop,
    handlePaste,
    resetArtifactForm,
    attachArtifact,
    openImagePreview,
    closeImagePreview,
    isStoredArtifact,
    isStoredImage,
    isStoredVideo,
    isStoredAudio,
    isStoredNonImage,
    isStoredText,
    toggleArtifactPreview,
    deleteArtifact,
    copyArtifactRef,
  };
}

export type ArtifactsModule = ReturnType<typeof createArtifactsModule>;
