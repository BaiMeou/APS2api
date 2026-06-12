from __future__ import annotations
from pydantic import BaseModel, Field, ConfigDict
from typing import Any, TYPE_CHECKING, Optional

if TYPE_CHECKING:
    pass

class AppConfig(BaseModel):
    port_api: int = 2156
    max_retries: int = 2
    error_dir: str = "errors"
    debug: bool = False
    log_dir: str = "logs"
    admin_password: str = ""  # 管理面板密码，留空则首次启动自动生成并打印到日志
    proxy_url: str = ""  # 出站代理 URL，优先级低于 PROXY_URL 环境变量
    subscription_url: str = ""  # 上次填入的机场订阅地址（用于面板自动回填）
    subscriptions: list[dict[str, Any]] = []  # 多订阅列表，由管理面板维护
    active_node_uri: str = ""  # 当前激活的节点 URI（容器重启后会自动恢复）
    active_node_name: str = ""  # 当前激活节点的显示名
    node_pool: list[dict[str, Any]] = []  # 节点轮换池
    node_pool_index: int = 0  # 当前轮换池索引
    anti429_enabled: bool = True  # 随机数防429开关
    drop_max_tokens: bool = True  # 请求防护开关，管理页面维护
    anti429_target: str = "system"  # 插入位置：system 或 user
    force_no_stream: bool = False  # 强制关闭流式输出（客户端 stream=true 也会被转成非流式）
    safety_settings: dict[str, str] = Field(default_factory=dict)  # 自定义 safety threshold 配置
    parallel_pool_enabled: bool = True  # 节点池并行请求开关：开启后同时尝试多个节点，谁先成功就用谁
    parallel_pool_size: int = 4  # 并行请求池大小，自用场景默认宽松一些
    parallel_pool_max_size: int = 12  # 防止误配置过大导致资源耗尽
    parallel_worker_base_port: int = 12080  # 并行临时 worker 起始端口，用于 vmess/vless/trojan 等订阅节点
    parallel_worker_port_span: int = 2000  # 并行临时 worker 可申请的端口范围大小
    business_session_concurrency_limit: int = 0  # 活动业务会话最大数量，0 表示不限
    parallel_node_top_k: int = 80  # 每次从健康评分最高的一批节点里加权随机选择，避免固定顺序踩坑
    parallel_pool_max_rounds: int = 0  # 滚动请求池最大候选轮次，0 表示不限轮次
    parallel_pool_deadline_seconds: float = 0  # 滚动请求池等待首包总超时，0 表示不限时
    vertex_api_key: str = "AIzaSyCI-zsRP85UVOi0DjtiCwWBwQ1djDy741g"  # Vertex AI 匿名接口 API key
    stream_query_signature: str = "2/l8eCsMMY49imcDQ/lwwXyL8cYtTjxZBF2dNqy69LodY="  # 流式 GraphQL query signature
    count_tokens_query_signature: str = "2/mENOSldfC+HZM+tGhVuJLrl8M6gEyK3HRjUKuA5AM58="  # CountTokens GraphQL query signature

    model_config = ConfigDict(extra="ignore")

class APIKeyInfo(BaseModel):
    name: str
    is_active: bool

# JSON Schema 相关类型
class JSONSchemaProperty(BaseModel):
    type: str
    description: str | None = None
    enum: list[str] | None = None
    items: dict[str, Any] | None = None
    properties: dict[str, Any] | None = None
    required: list[str] | None = None
    
    model_config = ConfigDict(extra="allow")

class JSONSchema(BaseModel):
    type: str
    properties: dict[str, JSONSchemaProperty] | None = None
    required: list[str] | None = None
    description: str | None = None
    
    model_config = ConfigDict(extra="allow")

class GenerationConfig(BaseModel):
    maxOutputTokens: int | None = Field(None, alias="maxOutputTokens")
    stopSequences: list[str] | None = Field(None, alias="stopSequences")
    topP: float | None = Field(None, alias="topP")
    topK: int | None = Field(None, alias="topK")
    responseMimeType: str | None = Field(None, alias="responseMimeType")
    responseSchema: JSONSchema | None = Field(None, alias="responseSchema")
    candidateCount: int | None = Field(None, alias="candidateCount")
    presencePenalty: float | None = Field(None, alias="presencePenalty")
    frequencyPenalty: float | None = Field(None, alias="frequencyPenalty")
    responseLogprobs: bool | None = Field(None, alias="responseLogprobs")
    speechConfig: dict[str, str | int | float] | None = Field(None, alias="speechConfig")
    audioTimestamp: bool | None = Field(None, alias="audioTimestamp")
    enableEnhancedCivicAnswers: bool | None = Field(None, alias="enableEnhancedCivicAnswers")
    responseModalities: list[str] | None = Field(None, alias="responseModalities")
    imageConfig: dict[str, Any] | None = Field(None, alias="imageConfig")
    mediaResolution: str | None = Field(None, alias="mediaResolution")
    thinkingConfig: dict[str, Any] | None = Field(None, alias="thinkingConfig")
    logprobs: int | None = None
    routingConfig: dict[str, Any] | None = Field(None, alias="routingConfig")

    model_config = ConfigDict(populate_by_name=True, extra="allow")

class SafetySetting(BaseModel):
    category: str
    threshold: str

# Gemini API 相关类型定义
class FunctionCall(BaseModel):
    name: str
    args: dict[str, Any]
    
    model_config = ConfigDict(extra="allow")

class FunctionResponse(BaseModel):
    name: str
    response: dict[str, Any]
    
    model_config = ConfigDict(extra="allow")

class InlineData(BaseModel):
    mimeType: str = Field(alias="mimeType")
    data: str
    
    model_config = ConfigDict(populate_by_name=True)

class FileData(BaseModel):
    mimeType: str = Field(alias="mimeType")
    fileUri: str = Field(alias="fileUri")
    
    model_config = ConfigDict(populate_by_name=True)

class ContentPart(BaseModel):
    text: str | None = None
    functionCall: FunctionCall | None = Field(None, alias="functionCall")
    functionResponse: FunctionResponse | None = Field(None, alias="functionResponse")
    inlineData: InlineData | None = Field(None, alias="inlineData")
    fileData: FileData | None = Field(None, alias="fileData")
    executableCode: dict[str, Any] | None = Field(None, alias="executableCode")
    codeExecutionResult: dict[str, Any] | None = Field(None, alias="codeExecutionResult")
    videoMetadata: dict[str, Any] | None = Field(None, alias="videoMetadata")
    mediaResolution: str | None = Field(None, alias="mediaResolution")
    thought: str | bool | None = None
    thoughtSignature: str | None = Field(None, alias="thoughtSignature")
    
    model_config = ConfigDict(extra="allow", populate_by_name=True)

class Content(BaseModel):
    parts: list[ContentPart]
    role: str | None = None
    
    model_config = ConfigDict(extra="allow")

class FunctionDeclaration(BaseModel):
    name: str
    description: str | None = None
    parameters: JSONSchema | None = None
    
    model_config = ConfigDict(extra="allow")

class Tool(BaseModel):
    functionDeclarations: list[FunctionDeclaration] | None = Field(None, alias="functionDeclarations")
    googleSearch: dict[str, Any] | None = Field(None, alias="googleSearch")
    googleSearchRetrieval: dict[str, Any] | None = Field(None, alias="googleSearchRetrieval")
    codeExecution: dict[str, Any] | None = Field(None, alias="codeExecution")
    retrieval: dict[str, Any] | None = None
    urlContext: dict[str, Any] | None = Field(None, alias="urlContext")
    
    model_config = ConfigDict(extra="allow", populate_by_name=True)

class FunctionCallingConfig(BaseModel):
    mode: str | None = None
    allowedFunctionNames: list[str] | None = Field(None, alias="allowedFunctionNames")
    disable: bool | None = None
    
    model_config = ConfigDict(extra="allow", populate_by_name=True)

class ToolConfig(BaseModel):
    functionCallingConfig: FunctionCallingConfig | None = Field(None, alias="functionCallingConfig")
    
    model_config = ConfigDict(extra="allow", populate_by_name=True)

class SystemInstruction(BaseModel):
    parts: list[ContentPart]
    
    model_config = ConfigDict(extra="allow")

class SafetyRating(BaseModel):
    category: str
    probability: str
    blocked: bool | None = None
    
    model_config = ConfigDict(extra="allow")

class CitationSource(BaseModel):
    startIndex: int | None = Field(None, alias="startIndex")
    endIndex: int | None = Field(None, alias="endIndex")
    uri: str | None = None
    license: str | None = None
    
    model_config = ConfigDict(extra="allow", populate_by_name=True)

class CitationMetadata(BaseModel):
    citationSources: list[CitationSource] | None = Field(None, alias="citationSources")
    
    model_config = ConfigDict(extra="allow", populate_by_name=True)

class GroundingChunk(BaseModel):
    web: dict[str, str] | None = None
    retrievedContext: dict[str, str] | None = Field(None, alias="retrievedContext")
    
    model_config = ConfigDict(extra="allow", populate_by_name=True)

class GroundingSupport(BaseModel):
    segment: dict[str, int | str] | None = None
    groundingChunkIndices: list[int] | None = Field(None, alias="groundingChunkIndices")
    confidenceScores: list[float] | None = Field(None, alias="confidenceScores")
    
    model_config = ConfigDict(extra="allow", populate_by_name=True)

class SearchEntryPoint(BaseModel):
    renderedContent: str | None = Field(None, alias="renderedContent")
    sdkBlob: str | None = Field(None, alias="sdkBlob")
    
    model_config = ConfigDict(extra="allow", populate_by_name=True)

class RetrievalMetadata(BaseModel):
    googleSearchDynamicRetrievalScore: float | None = Field(None, alias="googleSearchDynamicRetrievalScore")
    
    model_config = ConfigDict(extra="allow", populate_by_name=True)

class GroundingMetadata(BaseModel):
    groundingChunks: list[GroundingChunk] | None = Field(None, alias="groundingChunks")
    groundingSupports: list[GroundingSupport] | None = Field(None, alias="groundingSupports")
    webSearchQueries: list[str] | None = Field(None, alias="webSearchQueries")
    searchEntryPoint: SearchEntryPoint | None = Field(None, alias="searchEntryPoint")
    retrievalMetadata: RetrievalMetadata | None = Field(None, alias="retrievalMetadata")
    
    model_config = ConfigDict(extra="allow", populate_by_name=True)

class UsageMetadata(BaseModel):
    promptTokenCount: int | None = Field(None, alias="promptTokenCount")
    candidatesTokenCount: int | None = Field(None, alias="candidatesTokenCount")
    totalTokenCount: int | None = Field(None, alias="totalTokenCount")
    cachedContentTokenCount: int | None = Field(None, alias="cachedContentTokenCount")
    
    model_config = ConfigDict(extra="allow", populate_by_name=True)

class PromptFeedback(BaseModel):
    blockReason: str | None = Field(None, alias="blockReason")
    safetyRatings: list[SafetyRating] | None = Field(None, alias="safetyRatings")
    
    model_config = ConfigDict(extra="allow", populate_by_name=True)

class Candidate(BaseModel):
    content: Content | None = None
    finishReason: str | None = Field(None, alias="finishReason")
    index: int | None = None
    safetyRatings: list[SafetyRating] | None = Field(None, alias="safetyRatings")
    citationMetadata: CitationMetadata | None = Field(None, alias="citationMetadata")
    groundingMetadata: GroundingMetadata | None = Field(None, alias="groundingMetadata")
    tokenCount: int | None = Field(None, alias="tokenCount")
    avgLogprobs: float | None = Field(None, alias="avgLogprobs")
    
    model_config = ConfigDict(extra="allow", populate_by_name=True)

class GeminiResponse(BaseModel):
    candidates: list[Candidate] | None = None
    promptFeedback: PromptFeedback | None = Field(None, alias="promptFeedback")
    usageMetadata: UsageMetadata | None = Field(None, alias="usageMetadata")
    createTime: str | None = Field(None, alias="createTime")
    modelVersion: str | None = Field(None, alias="modelVersion")
    responseId: str | None = Field(None, alias="responseId")
    
    model_config = ConfigDict(extra="allow", populate_by_name=True)

class GeminiPayload(BaseModel):
    contents: list[Content]
    tools: list[Tool] | None = None
    toolConfig: ToolConfig | None = Field(None, alias="toolConfig")
    systemInstruction: SystemInstruction | None = Field(None, alias="systemInstruction")
    safetySettings: list[SafetySetting] | None = Field(None, alias="safetySettings")
    generationConfig: GenerationConfig | None = Field(None, alias="generationConfig")
    cachedContent: str | None = Field(None, alias="cachedContent")
    labels: dict[str, str] | None = None
    
    model_config = ConfigDict(extra="allow", populate_by_name=True)

# 流处理相关类型
class StreamState(BaseModel):
    finish_reason: str | None = None
    safety_ratings: list[dict[str, Any]] = Field(default_factory=list)
    citation_metadata: dict[str, Any] = Field(default_factory=dict)
    usage_metadata: dict[str, Any] = Field(default_factory=dict)
    grounding_metadata: dict[str, Any] = Field(default_factory=dict)
    token_count: int | None = None
    avg_logprobs: float | None = None
    candidate_index: int = 0
    create_time: str | None = None
    model_version: str | None = None
    prompt_feedback: dict[str, Any] = Field(default_factory=dict)
    response_id: str | None = None
    has_error: bool = False
    error_message: str = ""
    parts_by_path: dict[str, ContentPart] = Field(default_factory=dict)
    unindexed_parts: list[dict[str, Any]] = Field(default_factory=list)
    
    model_config = ConfigDict(extra="allow")

# Vertex AI 内部请求格式
class VertexVariables(BaseModel):
    model: str
    contents: list[Content]
    tools: list[Tool] | None = None
    toolConfig: ToolConfig | None = Field(None, alias="toolConfig")
    systemInstruction: SystemInstruction | None = Field(None, alias="systemInstruction")
    safetySettings: list[SafetySetting] | None = Field(None, alias="safetySettings")
    generationConfig: GenerationConfig | None = Field(None, alias="generationConfig")
    region: str | None = None
    recaptchaToken: str | None = Field(None, alias="recaptchaToken")
    
    model_config = ConfigDict(extra="allow", populate_by_name=True)

class VertexRequest(BaseModel):
    querySignature: str | None = Field(None, alias="querySignature")
    operationName: str | None = Field(None, alias="operationName")
    variables: VertexVariables
    
    model_config = ConfigDict(extra="allow", populate_by_name=True)

# 请求上下文类型
class RequestContext(BaseModel):
    downstream_payload: GeminiPayload
    upstream_payload: VertexRequest
    
    model_config = ConfigDict(extra="allow")
