/**
 * Runbooks —— 处置手册页（能力域 9）。
 * 列表 + 详情（content_markdown 用 react-markdown 渲染）+ 创建 + execute（human-in-the-loop）。
 * 后端：GET/POST /runbooks，GET/DELETE /runbooks/:id，POST /runbooks/:id/execute。
 */
import { useState } from "react";
import ReactMarkdown from "react-markdown";
import { ArrowLeft, BookOpen, Play, Plus, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import {
  useCreateRunbook,
  useDeleteRunbook,
  useExecuteRunbook,
  useRunbook,
  useRunbooks,
} from "@/hooks/runbooks";
import { formatTime } from "@/lib/format";
import type { Runbook } from "@/lib/types";

export function Runbooks() {
  const { data, isLoading, isError } = useRunbooks();
  const [selected, setSelected] = useState<number | undefined>(undefined);
  const [creating, setCreating] = useState(false);

  if (selected) return <RunbookDetail id={selected} onBack={() => setSelected(undefined)} />;

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Runbook</h1>
          <p className="text-sm text-muted-foreground">
            处置手册：诊断类（只读）Vigil 内置执行；处置类（写操作）须人确认或对接外部平台。
          </p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="mr-1 h-4 w-4" /> 创建 Runbook
        </Button>
      </div>

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-16 w-full" />
          ))}
        </div>
      ) : isError ? null : !data || data.length === 0 ? (
        <Card>
          <CardContent className="p-6">
            <EmptyState
              icon={<BookOpen className="h-8 w-8" />}
              title="还没有 Runbook"
              description="创建处置手册，告警触发时自动展示处置步骤。"
            />
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-3">
          {data.map((r) => (
            <Card
              key={r.id}
              className="cursor-pointer transition-colors hover:bg-accent/30"
              onClick={() => setSelected(r.id)}
            >
              <CardContent className="flex items-center justify-between p-4">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="font-medium">{r.name}</span>
                    <Badge variant="outline" className="text-xs">
                      {r.type === "executable" ? "可执行" : "文档"}
                    </Badge>
                  </div>
                  {r.content_markdown && (
                    <p className="mt-1 truncate text-xs text-muted-foreground">
                      {r.content_markdown.slice(0, 80)}
                    </p>
                  )}
                </div>
                <div className="flex items-center gap-2">
                  <span className="text-xs text-muted-foreground">{formatTime(r.created_at)}</span>
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      {creating && <CreateRunbookDialog onClose={() => setCreating(false)} />}
    </div>
  );
}

/** RunbookDetail 详情：markdown 渲染 + 执行（human-in-the-loop）+ 删除。 */
function RunbookDetail({ id, onBack }: { id: number; onBack: () => void }) {
  const { data: rb, isLoading, isError } = useRunbook(id);
  const del = useDeleteRunbook();
  const exec = useExecuteRunbook();
  const [executing, setExecuting] = useState(false);
  const [incidentId, setIncidentId] = useState("");

  if (isLoading) return <div className="p-6"><Skeleton className="h-40 w-full" /></div>;
  if (isError || !rb) {
    return (
      <div className="p-6">
        <Button variant="ghost" onClick={onBack}><ArrowLeft className="mr-1 h-4 w-4" />返回</Button>
        <EmptyState title="Runbook 不存在" />
      </div>
    );
  }

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <Button variant="ghost" onClick={onBack}>
          <ArrowLeft className="mr-1 h-4 w-4" />返回列表
        </Button>
        <div className="flex gap-2">
          <Button onClick={() => setExecuting(true)}>
            <Play className="mr-1 h-4 w-4" /> 执行
          </Button>
          <Button
            variant="outline"
            onClick={() => {
              del.mutate(id, { onSuccess: onBack });
            }}
            disabled={del.isPending}
          >
            <Trash2 className="mr-1 h-4 w-4" /> 删除
          </Button>
        </div>
      </div>

      <div>
        <div className="flex items-center gap-2">
          <h1 className="text-2xl font-semibold tracking-tight">{rb.name}</h1>
          <Badge variant="outline">{rb.type === "executable" ? "可执行" : "文档"}</Badge>
        </div>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">处置步骤</CardTitle>
        </CardHeader>
        <CardContent>
          {rb.content_markdown ? (
            <div className="prose prose-sm max-w-none text-sm">
              <ReactMarkdown>{rb.content_markdown}</ReactMarkdown>
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">（无内容）</p>
          )}
        </CardContent>
      </Card>

      {executing && (
        <Dialog open onClose={() => setExecuting(false)} title="执行 Runbook" description="可执行 Runbook 写操作需人确认（human-in-the-loop）。">
          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault();
              exec.mutate(
                { id, incidentId: Number(incidentId), approved: true },
                { onSuccess: () => setExecuting(false) },
              );
            }}
          >
            <label className="block space-y-1">
              <span className="text-xs font-medium text-muted-foreground">事件 ID</span>
              <Input
                type="number"
                value={incidentId}
                onChange={(e) => setIncidentId(e.target.value)}
                required
                placeholder="42"
              />
            </label>
            <p className="rounded-md bg-muted p-2 text-xs text-muted-foreground">
              ⚠️ 写操作将执行，请确认 Runbook 步骤安全。诊断类（readonly）自动执行无需确认。
            </p>
            <div className="flex justify-end gap-2">
              <Button type="button" variant="outline" onClick={() => setExecuting(false)}>取消</Button>
              <Button type="submit" disabled={exec.isPending || !incidentId}>确认执行</Button>
            </div>
          </form>
        </Dialog>
      )}
    </div>
  );
}

/** CreateRunbookDialog 创建文档式 Runbook。 */
function CreateRunbookDialog({ onClose }: { onClose: () => void }) {
  const create = useCreateRunbook();
  const [name, setName] = useState("");
  const [type, setType] = useState<Runbook["type"]>("document");
  const [content, setContent] = useState("");

  return (
    <Dialog open onClose={onClose} title="创建 Runbook" description="文档式（Markdown）或可执行式。">
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate(
            { name, type, content_markdown: content },
            { onSuccess: onClose },
          );
        }}
      >
        <label className="block space-y-1">
          <span className="text-xs font-medium text-muted-foreground">名称</span>
          <Input value={name} onChange={(e) => setName(e.target.value)} required />
        </label>
        <label className="block space-y-1">
          <span className="text-xs font-medium text-muted-foreground">类型</span>
          <Select value={type} onChange={(e) => setType(e.target.value as Runbook["type"])}>
            <option value="document">文档（Markdown）</option>
            <option value="executable">可执行</option>
          </Select>
        </label>
        <label className="block space-y-1">
          <span className="text-xs font-medium text-muted-foreground">内容（Markdown）</span>
          <Textarea
            value={content}
            onChange={(e) => setContent(e.target.value)}
            rows={8}
            placeholder={"# 处置步骤\n1. ...\n2. ..."}
          />
        </label>
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="outline" onClick={onClose}>取消</Button>
          <Button type="submit" disabled={create.isPending || !name}>创建</Button>
        </div>
      </form>
    </Dialog>
  );
}
