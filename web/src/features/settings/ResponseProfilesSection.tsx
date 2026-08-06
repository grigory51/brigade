import { useCallback, useEffect, useState } from "react";
import { Loader2, Pencil, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { responseProfileClient } from "@/api/client";
import type { ResponseProfile } from "@/api/gen/brigade/v1/response_profile_pb";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Description, FieldLabel, Loading, SectionHeader, errorText } from "./ui";

type Draft = { id: string; name: string; instructions: string };

export function ResponseProfilesSection() {
  const [profiles, setProfiles] = useState<ResponseProfile[] | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saving, setSaving] = useState(false);

  const reload = useCallback(async () => {
    const result = await responseProfileClient.list({});
    setProfiles(result.profiles);
  }, []);

  useEffect(() => {
    void reload().catch(() => setProfiles([]));
  }, [reload]);

  const save = useCallback(async () => {
    if (!draft) return;
    setSaving(true);
    try {
      if (draft.id) {
        await responseProfileClient.update(draft);
      } else {
        await responseProfileClient.create(draft);
      }
      setDraft(null);
      await reload();
      toast.success("Профиль сохранён");
    } catch (error) {
      toast.error(errorText(error, "Не удалось сохранить профиль"));
    } finally {
      setSaving(false);
    }
  }, [draft, reload]);

  const remove = useCallback(async (profile: ResponseProfile) => {
    try {
      await responseProfileClient.delete({ id: profile.id });
      await reload();
      toast.success(`Профиль ${profile.name} удалён`);
    } catch (error) {
      toast.error(errorText(error, "Не удалось удалить профиль"));
    }
  }, [reload]);

  if (profiles === null) return <Loading />;

  return (
    <>
      <SectionHeader title="Профили ответов">
        <Description>
          Профиль задаёт тон и подробность ответов в конкретной ACP-сессии. Изменения
          применятся к уже выбранному профилю после следующего перезапуска агента.
        </Description>
      </SectionHeader>

      <div className="flex flex-col gap-2">
        {profiles.map((profile) => (
          <div key={profile.id} className="flex items-start gap-3 rounded-[10px] border bg-card px-3 py-2.5">
            <div className="min-w-0 flex-1">
              <div className="text-[13px] font-medium">{profile.name}</div>
              <div className="line-clamp-2 text-[11.5px] leading-relaxed text-muted-foreground">
                {profile.instructions || "Без дополнительных инструкций — агент отвечает как обычно."}
              </div>
            </div>
            {!profile.readOnly && (
              <>
                <button type="button" aria-label="Изменить" onClick={() => setDraft({ id: profile.id, name: profile.name, instructions: profile.instructions })} className="flex size-7 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-secondary hover:text-foreground">
                  <Pencil className="size-3.5" />
                </button>
                <button type="button" aria-label="Удалить" onClick={() => void remove(profile)} className="flex size-7 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-secondary hover:text-destructive">
                  <Trash2 className="size-3.5" />
                </button>
              </>
            )}
          </div>
        ))}
        {!draft && (
          <Button variant="outline" className="self-start" onClick={() => setDraft({ id: "", name: "", instructions: "" })}>
            <Plus className="size-4" /> Добавить профиль
          </Button>
        )}
      </div>

      {draft && (
        <div className="flex flex-col gap-4 rounded-[12px] border bg-card p-4">
          <div className="space-y-2">
            <FieldLabel>Название</FieldLabel>
            <Input value={draft.name} maxLength={80} onChange={(event) => setDraft({ ...draft, name: event.target.value })} />
          </div>
          <div className="space-y-2">
            <div className="flex justify-between"><FieldLabel>Инструкции</FieldLabel><span className="text-[11px] text-muted-foreground">{draft.instructions.length}/2000</span></div>
            <Textarea value={draft.instructions} maxLength={2000} rows={7} onChange={(event) => setDraft({ ...draft, instructions: event.target.value })} />
          </div>
          <div className="flex justify-end gap-2 border-t pt-3">
            <Button variant="outline" disabled={saving} onClick={() => setDraft(null)}>Отмена</Button>
            <Button disabled={saving || !draft.name.trim() || !draft.instructions.trim()} onClick={() => void save()}>
              {saving && <Loader2 className="size-4 animate-spin" />} Сохранить
            </Button>
          </div>
        </div>
      )}
    </>
  );
}
